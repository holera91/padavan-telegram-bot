package main

import (
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/PuerkitoBio/goquery"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
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

var (
	cache CachedRates
	loc   *time.Location
)

func main() {
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
			if update.Message == nil {
				continue
			}

			// Log received message
			log.Printf("[%s] %s", update.Message.From.UserName, update.Message.Text)

			// Handle commands
			switch update.Message.Command() {
			case "start":
				sendMessage(bot, update.Message.Chat.ID,
					"👋 Вітаю! Цей бот показує актуальні курси валют.\n\n"+
						"Доступна команда:\n/rates - поточні курси")

			case "rates":
				currentRates := getCurrentRates()
				if len(currentRates.Rates) == 0 {
					sendMessage(bot, update.Message.Chat.ID, "⏳ Дані завантажуються...")
					continue
				}
				sendRates(bot, update.Message.Chat.ID, currentRates)

			default:
				sendMessage(bot, update.Message.Chat.ID, "Використовуйте /rates для отримання курсів")
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
