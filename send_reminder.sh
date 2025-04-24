#!/bin/bash

# Шлях до директорії з рахунками
INVOICE_DIR="/media/xi/life-invoice"

# Отримуємо поточний місяць та рік
CURRENT_MONTH=$(date +"%B")
CURRENT_YEAR=$(date +"%Y")

# Формуємо назву файлу
INVOICE_FILE="$INVOICE_DIR/$CURRENT_MONTH $CURRENT_YEAR.json"

# Перевіряємо чи існує файл
if [ ! -f "$INVOICE_FILE" ]; then
    echo "Файл $INVOICE_FILE не знайдено"
    exit 1
fi

# Перевіряємо чи було вже відправлено нагадування в цьому місяці
if grep -q "\"відправлено нагадування\"" "$INVOICE_FILE"; then
    echo "Нагадування вже було відправлено в цьому місяці"
    exit 0
fi

# Відправляємо нагадування
export TELEGRAM_BOT_TOKEN="ваш токен"
export TARGET_CHAT_ID="ID групи"
./padavan-bot -send

# Якщо відправка успішна, додаємо інформацію про відправку в JSON
if [ $? -eq 0 ]; then
    # Створюємо тимчасовий файл
    TMP_FILE=$(mktemp)
    
    # Додаємо новий рядок перед останньою дужкою
    sed '$i\  "відправлено нагадування": "'$(date +"%Y-%m-%d %H:%M:%S")'"' "$INVOICE_FILE" > "$TMP_FILE"
    
    # Замінюємо оригінальний файл
    mv "$TMP_FILE" "$INVOICE_FILE"
    
    echo "Нагадування успішно відправлено та додано в $INVOICE_FILE"
else
    echo "Помилка відправки нагадування"
    exit 1
fi 