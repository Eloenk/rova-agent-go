git pull
go build -o she ./cmd/server
go build -o wah ./cmd/whatsapp-bot
pm2 restart hoe
pm2 restart wah
