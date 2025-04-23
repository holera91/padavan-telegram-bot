зробитм щоб бот реагував тільки на комани а не на всі повідомлення
# Telegram Bot для роутера Padavan с курсами валют

Этот бот получает курсы валют с сайта Rulya Bank и кеширует их, обновляя данные каждый час. Работает на роутерах с прошивкой Padavan.

## 📦 Установка и настройка

### Требования
- Роутер с прошивкой Padavan
- Go 1.16+ (для компиляции)
- SSH-доступ к роутеру

### Компиляция
1. Установите зависимости:
```bash
go get github.com/PuerkitoBio/goquery
go get github.com/go-telegram-bot-api/telegram-bot-api/v5
```

2. Скомпилируйте для MIPS (архитектура большинства роутеров):
```bash
env GOOS=linux GOARCH=mipsle GOMIPS=softfloat go build -ldflags="-s -w" -o padavan-bot main.go
```

3. (Опционально) Сожмите бинарник:
```bash
upx --best padavan-bot
```

## 🚀 Развертывание на роутере

1. Скопируйте бинарник на роутер:
```bash
scp padavan-bot admin@192.168.1.171:/opt/home/admin/
```

2. Дайте права на выполнение:
```bash
chmod +x /opt/home/admin/padavan-bot
```


## ⚙️ Настройка автозагрузки

### Через SSH
Добавьте в `vi /etc/storage/post_wan_script.sh`:
```bash
export TELEGRAM_BOT_TOKEN="ваш токен"
/opt/home/admin/padavan-bot > /dev/null 2>&1 &
```

## 🛠 Управление ботом

### Проверить статус
```bash
ps | grep padavan-bot
```

### Остановить бот
```bash
killall padavan-bot
```

### Просмотреть логи
```bash
cat /tmp/bot.log  # если логи включены
```

## ❌ Удаление из автозагрузки

1. Удалите строку запуска из файла  `vi /etc/storage/post_wan_script.sh`

2. Сохраните изменения:
`:wq`

3. Перезагрузите роутер:
```bash
reboot
```

## 🔄 Обновление бота
1. Остановите текущую версию
2. Замените бинарник
3. Перезапустите:
```bash
killall padavan-bot
/opt/home/admin/padavan-bot > /dev/null 2>&1 &
```

## 📝 Примечания
- Бот автоматически обновляет курсы каждые 60 минут
- Для работы требуется интернет-подключение
- Команда в Telegram: `/rates` - получить текущие курсы валют

## ⚠️ Безопасность
- Не храните токен в открытом виде
- Ограничьте доступ к боту через настройки Telegram (@BotFather)