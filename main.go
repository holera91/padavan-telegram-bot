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
)

func main() {
	// Set Kyiv timezone
	loc, err := time.LoadLocation("Europe/Kiev")
	if err != nil {
		log.Printf("Failed to load timezone: %v, using UTC", err)
		loc = time.UTC
	}
	time.Local = loc

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

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	httpClient := &http.Client{Transport: tr}

	bot, err := tgbotapi.NewBotAPIWithClient(botToken, tgbotapi.APIEndpoint, httpClient)
	if err != nil {
		log.Fatal("Помилка ініціалізації бота: ", err)
	}

	bot.Debug = true
	log.Printf("Bot authorized as %s (server time: %s)", bot.Self.UserName, time.Now().Format("02.01.2006 15:04:05 MST"))

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case update := <-updates:
			if update.Message == nil {
				continue
			}

			log.Printf("[%s] %s (time: %s)", update.Message.From.UserName, update.Message.Text, time.Now().Format("15:04:05"))

			switch update.Message.Command() {
			case "start":
				sendMessage(bot, update.Message.Chat.ID,
					"👋 Вітаю! Я бот для відстеження курсів валют.\n\n"+
						"Доступні команди:\n"+
						"/rates - поточні курси валют\n"+
						"/time - перевірити час на сервері")

			case "rates":
				currentRates := getCurrentRates()
				if len(currentRates.Rates) == 0 {
					sendMessage(bot, update.Message.Chat.ID, "⏳ Курси ще не завантажено, спробуйте через хвилину")
					continue
				}
				sendRates(bot, update.Message.Chat.ID, currentRates)

			case "time":
				sendMessage(bot, update.Message.Chat.ID,
					fmt.Sprintf("🕒 Час на сервері: %s",
						time.Now().Format("02.01.2006 15:04:05 MST")))

			default:
				sendMessage(bot, update.Message.Chat.ID,
					"Невідома команда. Введіть /start для довідки")
			}

		case <-sigChan:
			log.Println("Shutting down bot...")
			return
		}
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
	cache.LastUpdated = time.Now()
	log.Printf("Rates updated at %s", cache.LastUpdated.Format("02.01.2006 15:04:05 MST"))
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
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr}

	resp, err := client.Get("https://rulya-bank.com.ua/")
	if err != nil {
		return nil, fmt.Errorf("помилка запиту: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("статус код: %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("помилка парсингу HTML: %v", err)
	}

	var rates []CurrencyRate
	targetCurrencies := map[string]bool{"USD": true, "EUR": true, "PLZ": true}

	doc.Find("table tr").Each(func(i int, row *goquery.Selection) {
		if i == 0 {
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
		return nil, fmt.Errorf("курси не знайдено")
	}

	return rates, nil
}

func sendMessage(bot *tgbotapi.BotAPI, chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	if _, err := bot.Send(msg); err != nil {
		log.Printf("Помилка відправки: %v", err)
	}
}

func sendRates(bot *tgbotapi.BotAPI, chatID int64, rates CachedRates) {
	response := fmt.Sprintf("📊 Курси Rulya Bank (актуально на %s):\n\n",
		rates.LastUpdated.Format("02.01.2006 15:04:05 MST"))

	for _, rate := range rates.Rates {
		response += fmt.Sprintf("➡ %s: купівля %.2f, продаж %.2f (%s)\n",
			rate.Code, rate.Buy, rate.Sell, rate.Change)
	}

	response += "\n🔄 Автоматичне оновлення щогодини"
	sendMessage(bot, chatID, response)
}
