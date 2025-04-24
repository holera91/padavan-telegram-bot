package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/PuerkitoBio/goquery"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	// Текст повідомлення для відправки
	TARGET_MESSAGE = "Ваше повідомлення" // Замініть на ваш текст
)

type CurrencyRate struct {
	Code   string
	Buy    float64
	Sell   float64
	Change string
}

type CachedRates struct {
	Rates       []CurrencyRate
	LastUpdated time.Time
	mu          sync.RWMutex
}

type BillData struct {
	PaymentDue     string `json:"Термін оплати"`
	Amount         string `json:"Сума до сплати"`
	PaymentPurpose string `json:"Призначення платежу"`
}

var (
	cache CachedRates
	loc   *time.Location
)

func main() {
	// Parse command line arguments
	sendFlag := flag.Bool("send", false, "Відправити повідомлення через бота")
	flag.Parse()

	// Якщо вказано команду відправки повідомлення
	if *sendFlag {
		sendMessageOnly()
		return
	}

	// Initialize Kyiv timezone
	initTimezone()

	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		log.Fatal("Змінна середовища TELEGRAM_BOT_TOKEN не встановлена")
	}

	// Initialize cache
	cache = CachedRates{
		Rates:       make([]CurrencyRate, 0),
		LastUpdated: time.Time{},
	}

	// Start background updater
	go startBackgroundUpdater()

	// Create bot with custom HTTP client
	bot, err := tgbotapi.NewBotAPIWithClient(botToken, tgbotapi.APIEndpoint, &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	})
	if err != nil {
		log.Fatal("Помилка ініціалізації бота: ", err)
	}

	bot.Debug = true
	log.Printf("Бот %s запущений (час сервера: %s)", bot.Self.UserName, time.Now().In(loc).Format("02.01.2006 15:04"))

	// Configure updates
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	// Handle shutdown signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Main loop
	for {
		select {
		case update := <-updates:
			if update.Message == nil && update.CallbackQuery == nil {
				continue
			}

			// Обробка callback запитів
			if update.CallbackQuery != nil {
				handleCallback(bot, update.CallbackQuery)
				continue
			}

			// Log received message
			log.Printf("[%s] %s", update.Message.From.UserName, update.Message.Text)
			log.Printf("ID чату: %d", update.Message.Chat.ID)

			// Handle commands
			switch update.Message.Command() {
			case "start":
				sendMessage(bot, update.Message.Chat.ID,
					"👋 Вітаю! Цей бот показує актуальні курси валют.\n\n"+
						"Доступні команди:\n/rates - поточні курси\n/invoice - переглянути рахунки")

			case "rates":
				currentRates := getCurrentRates()
				if len(currentRates.Rates) == 0 {
					sendMessage(bot, update.Message.Chat.ID, "⏳ Дані завантажуються...")
					continue
				}
				sendRates(bot, update.Message.Chat.ID, currentRates)

			case "invoice":
				msg := createInvoiceMenu(update.Message.Chat.ID)
				bot.Send(msg)

			default:
				// Перевіряємо чи це відповідь на запит місяця
				parts := strings.Fields(update.Message.Text)
				if len(parts) > 0 {
					month := convertMonthToEnglish(parts[0])
					year := 0
					if len(parts) > 1 {
						year, _ = strconv.Atoi(parts[1])
					}

					bill, err := getBillForMonth(month, year)
					if err != nil {
						sendMessage(bot, update.Message.Chat.ID, fmt.Sprintf("❌ Помилка: %v", err))
						continue
					}
					message := formatMessage(bill)
					sendMessage(bot, update.Message.Chat.ID, message)
				} else {
					// Не відповідаємо на інші повідомлення
					continue
				}
			}

		case <-sigChan:
			log.Println("Завершення роботи бота...")
			return
		}
	}
}

func initTimezone() {
	var err error
	loc, err = time.LoadLocation("Europe/Kiev")
	if err != nil {
		// Fallback to UTC+3 if timezone loading fails
		loc = time.FixedZone("EET", 3*60*60)
		log.Printf("Використовується фіксований часовий пояс: UTC+3")
	}
}

func startBackgroundUpdater() {
	// Initial update
	updateRates()

	// Update every hour
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		updateRates()
	}
}

func updateRates() {
	newRates, err := fetchRatesFromSite()
	if err != nil {
		log.Printf("Помилка оновлення курсів: %v", err)
		return
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()

	cache.Rates = newRates
	cache.LastUpdated = time.Now().In(loc)
	log.Printf("Курси оновлено о %s", cache.LastUpdated.Format("15:04"))
}

func getCurrentRates() CachedRates {
	cache.mu.RLock()
	defer cache.mu.RUnlock()

	return CachedRates{
		Rates:       cache.Rates,
		LastUpdated: cache.LastUpdated,
	}
}

func fetchRatesFromSite() ([]CurrencyRate, error) {
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 10 * time.Second,
	}

	resp, err := client.Get("https://rulya-bank.com.ua/")
	if err != nil {
		return nil, fmt.Errorf("помилка запиту: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP статус: %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("помилка аналізу HTML: %v", err)
	}

	var rates []CurrencyRate
	targetCurrencies := map[string]bool{"USD": true, "EUR": true, "PLZ": true}

	doc.Find("table tr").Each(func(i int, row *goquery.Selection) {
		if i == 0 { // Skip header row
			return
		}

		cols := row.Find("td")
		if cols.Length() < 4 {
			return
		}

		currency := cols.Eq(1).Find("h3").Text()
		if !targetCurrencies[currency] {
			return
		}

		buy, err := strconv.ParseFloat(strings.TrimSpace(cols.Eq(2).Find("h3").Text()), 64)
		if err != nil {
			log.Printf("Помилка парсингу купівлі %s: %v", currency, err)
			return
		}

		sell, err := strconv.ParseFloat(strings.TrimSpace(cols.Eq(3).Find("h3").Text()), 64)
		if err != nil {
			log.Printf("Помилка парсингу продажу %s: %v", currency, err)
			return
		}

		change := ""
		if changeElem := cols.Eq(2).Find("sup font"); changeElem.Length() > 0 {
			change = strings.TrimSpace(changeElem.Text())
		}

		rates = append(rates, CurrencyRate{
			Code:   currency,
			Buy:    buy,
			Sell:   sell,
			Change: change,
		})
	})

	if len(rates) == 0 {
		return nil, fmt.Errorf("не знайдено курсів валют")
	}

	return rates, nil
}

func sendMessage(bot *tgbotapi.BotAPI, chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	if _, err := bot.Send(msg); err != nil {
		log.Printf("Помилка відправки повідомлення: %v", err)
	}
}

func sendRates(bot *tgbotapi.BotAPI, chatID int64, rates CachedRates) {
	// Format time with timezone abbreviation
	timeZone := "EET"
	if isDaylightSavingTime(rates.LastUpdated) {
		timeZone = "EEST"
	}

	response := fmt.Sprintf("📊 Курси валют (оновлено %s %s):\n\n",
		rates.LastUpdated.Format("02.01.2006 15:04"),
		timeZone)

	for _, rate := range rates.Rates {
		line := fmt.Sprintf("➡ %s: %.2f / %.2f", rate.Code, rate.Buy, rate.Sell)
		if rate.Change != "" {
			line += fmt.Sprintf(" (%s)", rate.Change)
		}
		response += line + "\n"
	}

	sendMessage(bot, chatID, response)
}

func isDaylightSavingTime(t time.Time) bool {
	// Ukraine switches to EEST at 03:00 on last Sunday in March
	// and back to EET at 04:00 on last Sunday in October
	year := t.Year()
	marchTime := time.Date(year, time.March, 31, 0, 0, 0, 0, loc)
	for marchTime.Weekday() != time.Sunday {
		marchTime = marchTime.AddDate(0, 0, -1)
	}
	marchTime = marchTime.Add(3 * time.Hour)

	octoberTime := time.Date(year, time.October, 31, 0, 0, 0, 0, loc)
	for octoberTime.Weekday() != time.Sunday {
		octoberTime = octoberTime.AddDate(0, 0, -1)
	}
	octoberTime = octoberTime.Add(4 * time.Hour)

	return t.After(marchTime) && t.Before(octoberTime)
}

// Функція для отримання останнього рахунку
func getLatestBill() (*BillData, error) {
	invoiceDir := "/media/xi/life-invoice"

	// Перевіряємо чи існує директорія
	if _, err := os.Stat(invoiceDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("директорія %s не існує", invoiceDir)
	}

	files, err := ioutil.ReadDir(invoiceDir)
	if err != nil {
		return nil, fmt.Errorf("помилка читання директорії %s: %v", invoiceDir, err)
	}

	var jsonFiles []string
	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(file.Name(), ".json") {
			jsonFiles = append(jsonFiles, filepath.Join(invoiceDir, file.Name()))
		}
	}

	if len(jsonFiles) == 0 {
		return nil, fmt.Errorf("не знайдено JSON файлів в директорії %s", invoiceDir)
	}

	// Сортуємо файли за датою модифікації (найновіший перший)
	sort.Slice(jsonFiles, func(i, j int) bool {
		infoI, _ := os.Stat(jsonFiles[i])
		infoJ, _ := os.Stat(jsonFiles[j])
		return infoI.ModTime().After(infoJ.ModTime())
	})

	// Читаємо найновіший файл
	data, err := ioutil.ReadFile(jsonFiles[0])
	if err != nil {
		return nil, fmt.Errorf("помилка читання файлу %s: %v", jsonFiles[0], err)
	}

	var bill BillData
	if err := json.Unmarshal(data, &bill); err != nil {
		return nil, fmt.Errorf("помилка парсингу JSON: %v", err)
	}

	return &bill, nil
}

// Функція для формування повідомлення
func formatMessage(bill *BillData) string {
	return fmt.Sprintf("📢 Нагадування заплатити за Life!\n\n"+
		"💰 До оплати: %s\n"+
		"⏰ Оплатити потрібно: %s",
		bill.Amount,
		bill.PaymentDue)
}

// Функція для відправки повідомлення без запуску основного процесу бота
func sendMessageOnly() {
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		log.Fatal("Змінна середовища TELEGRAM_BOT_TOKEN не встановлена")
	}

	chatIDStr := os.Getenv("TARGET_CHAT_ID")
	if chatIDStr == "" {
		log.Fatal("Змінна середовища TARGET_CHAT_ID не встановлена")
	}

	chatID, err := strconv.ParseInt(chatIDStr, 10, 64)
	if err != nil {
		log.Fatalf("Помилка парсингу ID чату: %v", err)
	}

	// Отримуємо останній рахунок
	bill, err := getLatestBill()
	if err != nil {
		log.Fatalf("Помилка отримання рахунку: %v", err)
	}

	// Формуємо повідомлення
	message := formatMessage(bill)

	// Create bot with custom HTTP client
	bot, err := tgbotapi.NewBotAPIWithClient(botToken, tgbotapi.APIEndpoint, &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	})
	if err != nil {
		log.Fatal("Помилка ініціалізації бота: ", err)
	}

	sendMessage(bot, chatID, message)
	log.Println("Повідомлення успішно відправлено")
}

// Функція для отримання рахунку за конкретний місяць
func getBillForMonth(month string, year int) (*BillData, error) {
	invoiceDir := "/media/xi/life-invoice"

	// Перевіряємо чи існує директорія
	if _, err := os.Stat(invoiceDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("директорія %s не існує", invoiceDir)
	}

	files, err := ioutil.ReadDir(invoiceDir)
	if err != nil {
		return nil, fmt.Errorf("помилка читання директорії %s: %v", invoiceDir, err)
	}

	var targetFile string
	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(file.Name(), ".json") {
			fileName := strings.ToLower(file.Name())
			if strings.Contains(fileName, strings.ToLower(month)) &&
				(year == 0 || strings.Contains(fileName, strconv.Itoa(year))) {
				targetFile = filepath.Join(invoiceDir, file.Name())
				break
			}
		}
	}

	if targetFile == "" {
		return nil, fmt.Errorf("не знайдено рахунку за %s %d", month, year)
	}

	data, err := ioutil.ReadFile(targetFile)
	if err != nil {
		return nil, fmt.Errorf("помилка читання файлу %s: %v", targetFile, err)
	}

	var bill BillData
	if err := json.Unmarshal(data, &bill); err != nil {
		return nil, fmt.Errorf("помилка парсингу JSON: %v", err)
	}

	return &bill, nil
}

// Функція для створення меню вибору
func createInvoiceMenu(chatID int64) tgbotapi.MessageConfig {
	msg := tgbotapi.NewMessage(chatID, "Оберіть опцію:")
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("За цей місяць", "invoice_current"),
			tgbotapi.NewInlineKeyboardButtonData("Вкажіть місяць", "invoice_custom"),
		),
	)
	return msg
}

// Функція для конвертації української назви місяця в англійську
func convertMonthToEnglish(month string) string {
	monthMap := map[string]string{
		"січень":   "January",
		"лютий":    "February",
		"березень": "March",
		"квітень":  "April",
		"травень":  "May",
		"червень":  "June",
		"липень":   "July",
		"серпень":  "August",
		"вересень": "September",
		"жовтень":  "October",
		"листопад": "November",
		"грудень":  "December",
		// Додаємо варіанти з великої літери
		"Січень":   "January",
		"Лютий":    "February",
		"Березень": "March",
		"Квітень":  "April",
		"Травень":  "May",
		"Червень":  "June",
		"Липень":   "July",
		"Серпень":  "August",
		"Вересень": "September",
		"Жовтень":  "October",
		"Листопад": "November",
		"Грудень":  "December",
	}

	if englishMonth, ok := monthMap[strings.ToLower(month)]; ok {
		return englishMonth
	}
	return month // Якщо місяць не знайдено, повертаємо оригінальне значення
}

// Функція для обробки callback запитів
func handleCallback(bot *tgbotapi.BotAPI, callback *tgbotapi.CallbackQuery) {
	switch callback.Data {
	case "invoice_current":
		bill, err := getLatestBill()
		if err != nil {
			sendMessage(bot, callback.Message.Chat.ID, fmt.Sprintf("❌ Помилка: %v", err))
			return
		}
		message := formatMessage(bill)
		sendMessage(bot, callback.Message.Chat.ID, message)

	case "invoice_custom":
		msg := tgbotapi.NewMessage(callback.Message.Chat.ID,
			"Введіть місяць у форматі:\n"+
				"- Тільки місяць (наприклад: Березень)\n"+
				"- Місяць та рік (наприклад: Березень 2025)")
		bot.Send(msg)
	}
}
