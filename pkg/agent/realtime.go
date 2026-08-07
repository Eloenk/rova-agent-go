package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type PhoenixMessage struct {
	Topic   string          `json:"topic"`
	Event   string          `json:"event"`
	Payload json.RawMessage `json:"payload"`
	Ref     string          `json:"ref,omitempty"`
}

type RealtimePostgresPayload struct {
	Type   string          `json:"type"` // "INSERT", "UPDATE", "DELETE"
	Schema string          `json:"schema"`
	Table  string          `json:"table"`
	Record json.RawMessage `json:"record"`
	Old    json.RawMessage `json:"old_record"`
}

type RealtimeClient struct {
	supabaseURL string
	supabaseKey string
	store       *Store
	conn        *websocket.Conn
	mu          sync.Mutex
	msgSeq      uint64
	stopChan    chan struct{}
}

func NewRealtimeClient(supabaseURL, supabaseKey string, store *Store) *RealtimeClient {
	return &RealtimeClient{
		supabaseURL: supabaseURL,
		supabaseKey: supabaseKey,
		store:       store,
		stopChan:    make(chan struct{}),
	}
}

func (rc *RealtimeClient) buildWssURL() (string, error) {
	parsed, err := url.Parse(rc.supabaseURL)
	if err != nil {
		return "", err
	}

	scheme := "wss"
	if parsed.Scheme == "http" {
		scheme = "ws"
	}

	host := parsed.Host
	wssPath := fmt.Sprintf("%s://%s/realtime/v1/websocket", scheme, host)
	return fmt.Sprintf("%s?apikey=%s&vsn=1.0.0", wssPath, rc.supabaseKey), nil
}

func (rc *RealtimeClient) nextRef() string {
	seq := atomic.AddUint64(&rc.msgSeq, 1)
	return fmt.Sprintf("%d", seq)
}

func (rc *RealtimeClient) Start(ctx context.Context) {
	if rc.supabaseURL == "" || rc.supabaseKey == "" {
		log.Println("[Realtime] Supabase URL or Anon Key missing. Realtime WebSocket disabled.")
		return
	}

	wssURL, err := rc.buildWssURL()
	if err != nil {
		log.Printf("[Realtime] Error building WebSocket URL: %v", err)
		return
	}

	go rc.reconnectLoop(ctx, wssURL)
}

func (rc *RealtimeClient) reconnectLoop(ctx context.Context, wssURL string) {
	backoff := 2 * time.Second

	for {
		select {
		case <-ctx.Done():
			log.Println("[Realtime] Context canceled. Closing Realtime WebSocket client...")
			rc.Close()
			return
		default:
		}

		log.Printf("[Realtime] Connecting to Supabase Realtime WebSocket (%s)...", rc.supabaseURL)
		
		headers := http.Header{}
		headers.Set("User-Agent", "Rova-Agent-Go/1.1 (Supabase Realtime)")

		conn, _, err := websocket.DefaultDialer.DialContext(ctx, wssURL, headers)
		if err != nil {
			log.Printf("[Realtime] WebSocket dial error: %v. Retrying in %v...", err, backoff)
			time.Sleep(backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}

		backoff = 2 * time.Second // Reset backoff on successful connection
		rc.mu.Lock()
		rc.conn = conn
		rc.mu.Unlock()

		log.Println("[Realtime] WebSocket Connected Successfully to Supabase Realtime!")

		// 1. Join Channels for agent_rules and standing_intents
		if err := rc.joinChannels(); err != nil {
			log.Printf("[Realtime] Channel join error: %v", err)
			conn.Close()
			continue
		}

		// 2. Initial state sync from Supabase PostgREST
		rc.store.syncInitialState()

		// 3. Start Heartbeat & Read Loops
		heartbeatStop := make(chan struct{})
		go rc.heartbeatLoop(heartbeatStop)

		err = rc.readLoop(ctx)
		close(heartbeatStop)

		rc.mu.Lock()
		if rc.conn != nil {
			rc.conn.Close()
			rc.conn = nil
		}
		rc.mu.Unlock()

		if err != nil {
			log.Printf("[Realtime] WebSocket connection closed: %v. Reconnecting...", err)
		}
		time.Sleep(backoff)
	}
}

func (rc *RealtimeClient) joinChannels() error {
	rulesJoin := PhoenixMessage{
		Topic: "realtime:public:agent_rules",
		Event: "phx_join",
		Ref:   rc.nextRef(),
		Payload: json.RawMessage(`{
			"config": {
				"postgres_changes": [
					{
						"event": "*",
						"schema": "public",
						"table": "agent_rules"
					}
				]
			}
		}`),
	}

	intentsJoin := PhoenixMessage{
		Topic: "realtime:public:standing_intents",
		Event: "phx_join",
		Ref:   rc.nextRef(),
		Payload: json.RawMessage(`{
			"config": {
				"postgres_changes": [
					{
						"event": "*",
						"schema": "public",
						"table": "standing_intents"
					}
				]
			}
		}`),
	}

	if err := rc.sendJSON(rulesJoin); err != nil {
		return err
	}
	return rc.sendJSON(intentsJoin)
}

func (rc *RealtimeClient) sendJSON(msg interface{}) error {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.conn == nil {
		return fmt.Errorf("websocket connection is nil")
	}
	return rc.conn.WriteJSON(msg)
}

func (rc *RealtimeClient) heartbeatLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			hb := PhoenixMessage{
				Topic:   "phoenix",
				Event:   "heartbeat",
				Payload: json.RawMessage(`{}`),
				Ref:     rc.nextRef(),
			}
			if err := rc.sendJSON(hb); err != nil {
				log.Printf("[Realtime] Heartbeat write error: %v", err)
				return
			}
		}
	}
}

func (rc *RealtimeClient) readLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		_, messageBytes, err := rc.conn.ReadMessage()
		if err != nil {
			return err
		}

		var msg PhoenixMessage
		if err := json.Unmarshal(messageBytes, &msg); err != nil {
			continue
		}

		if msg.Event == "postgres_changes" {
			rc.handlePostgresChange(msg.Payload)
		}
	}
}

func (rc *RealtimeClient) handlePostgresChange(payloadBytes json.RawMessage) {
	var payload RealtimePostgresPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		log.Printf("[Realtime] Error unmarshaling postgres_changes payload: %v", err)
		return
	}

	log.Printf("[Realtime] Live Event: Table %s, Event %s", payload.Table, payload.Type)

	switch payload.Table {
	case "agent_rules":
		rc.handleRuleChange(payload)
	case "standing_intents":
		rc.handleStandingIntentChange(payload)
	}
}

func (rc *RealtimeClient) handleRuleChange(payload RealtimePostgresPayload) {
	if payload.Type == "DELETE" {
		var oldDto supabaseRuleDto
		if err := json.Unmarshal(payload.Old, &oldDto); err == nil && oldDto.ID != "" {
			log.Printf("[Realtime Event] Rule DELETED: %s", oldDto.ID)
			rc.store.RemoveRuleFromRealtime(oldDto.ID)
		}
		return
	}

	var d supabaseRuleDto
	if err := json.Unmarshal(payload.Record, &d); err != nil {
		log.Printf("[Realtime] Error unmarshaling rule record: %v", err)
		return
	}

	rule := &AgentRule{
		ID:                  d.ID,
		Status:              d.Status,
		RecipientLabel:      d.RecipientLabel,
		RecipientIdentifier: d.RecipientIdentifier,
		Amount:              d.Amount,
		Pair:                d.Pair,
		TriggerType:         d.TriggerType,
		TriggerValue:        d.TriggerValue,
		CustodyMode:         d.CustodyMode,
		SourceWallet:        d.SourceWallet,
		NotifyPhone:         d.NotifyPhone,
		SourceChannel:       d.SourceChannel,
	}

	log.Printf("⚡ [Realtime Event] Rule %s (%s): %s %.4f -> %s %.2f", rule.ID, payload.Type, rule.Pair, rule.TriggerValue, rule.RecipientLabel, rule.Amount)
	rc.store.UpsertRuleFromRealtime(rule)
}

func (rc *RealtimeClient) handleStandingIntentChange(payload RealtimePostgresPayload) {
	if payload.Type == "DELETE" {
		var oldDto supabaseStandingIntentDto
		if err := json.Unmarshal(payload.Old, &oldDto); err == nil && oldDto.ID != "" {
			log.Printf("[Realtime Event] Standing Intent DELETED: %s", oldDto.ID)
			rc.store.RemoveStandingIntentFromRealtime(oldDto.ID)
		}
		return
	}

	var d supabaseStandingIntentDto
	if err := json.Unmarshal(payload.Record, &d); err != nil {
		log.Printf("[Realtime] Error unmarshaling standing intent record: %v", err)
		return
	}

	var lastRunAt *time.Time
	if d.LastRunAt != nil {
		if t, err := time.Parse(time.RFC3339, *d.LastRunAt); err == nil {
			lastRunAt = &t
		}
	}
	t, _ := time.Parse(time.RFC3339, d.CreatedAt)

	intent := &StandingIntent{
		ID:               d.ID,
		CreatedAt:        t,
		Status:           d.Status,
		IntentText:       d.IntentText,
		Plan:             d.Plan,
		Trigger:          d.Trigger,
		CustodyMode:      d.CustodyMode,
		SourceWallet:     d.SourceWallet,
		LastKnownBalance: d.LastKnownBalance,
		LastRunAt:        lastRunAt,
		RunCount:         d.RunCount,
		NotifyPhone:      d.NotifyPhone,
		SourceChannel:    d.SourceChannel,
	}

	log.Printf("⚡ [Realtime Event] Standing Intent %s (%s): \"%s\" (Status: %s)", intent.ID, payload.Type, intent.IntentText, intent.Status)
	rc.store.UpsertStandingIntentFromRealtime(intent)
}

func (rc *RealtimeClient) Close() {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.conn != nil {
		rc.conn.Close()
		rc.conn = nil
	}
}
