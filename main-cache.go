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

type RulyaRate struct {
	Currency string
	Buy      float64
	Sell     float64
	Change   string
}

type CachedRates struct {
	Rates       []RulyaRate
	LastUpdated time.Time
	mu          sync.RWMutex
}

var (
	cachedRates CachedRates
)

func main() {
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN environment variable not set")
	}

	// Инициализация кеша
	cachedRates = CachedRates{
		Rates:       make([]RulyaRate, 0),
		LastUpdated: time.Time{},
	}

	// Запускаем фоновое обновление курсов
	go startBackgroundUpdater()

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	httpClient := &http.Client{Transport: tr}

	bot, err := tgbotapi.NewBotAPIWithClient(botToken, tgbotapi.APIEndpoint, httpClient)
	if err != nil {
		log.Fatal(err)
	}

	bot.Debug = true
	log.Printf("Бот авторизован как %s", bot.Self.UserName)

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

			log.Printf("[%s] %s", update.Message.From.UserName, update.Message.Text)

			if update.Message.Command() == "rates" {
				rates := getCachedRates()
				if len(rates.Rates) == 0 {
					sendMessage(bot, update.Message.Chat.ID, "⏳ Курсы еще не загружены, попробуйте через минуту")
					continue
				}
				sendRates(bot, update.Message.Chat.ID, rates)
			} else {
				sendMessage(bot, update.Message.Chat.ID, "ℹ️ Доступная команда:\n/rates - получить курсы валют")
			}

		case <-sigChan:
			log.Println("Завершение работы бота...")
			return
		}
	}
}

func startBackgroundUpdater() {
	// Первое обновление при старте
	updateRates()

	// Обновление каждый час
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		updateRates()
	}
}

func updateRates() {
	rates, err := fetchRulyaRates()
	if err != nil {
		log.Printf("Ошибка обновления курсов: %v", err)
		return
	}

	cachedRates.mu.Lock()
	defer cachedRates.mu.Unlock()

	cachedRates.Rates = rates
	cachedRates.LastUpdated = time.Now()
	log.Printf("Курсы обновлены: %v", cachedRates.LastUpdated)
}

func getCachedRates() CachedRates {
	cachedRates.mu.RLock()
	defer cachedRates.mu.RUnlock()

	return CachedRates{
		Rates:       cachedRates.Rates,
		LastUpdated: cachedRates.LastUpdated,
	}
}

func fetchRulyaRates() ([]RulyaRate, error) {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr}

	resp, err := client.Get("https://rulya-bank.com.ua/")
	if err != nil {
		return nil, fmt.Errorf("ошибка запроса: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("статус код: %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ошибка парсинга HTML: %v", err)
	}

	var rates []RulyaRate
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
			log.Printf("Ошибка парсинга покупки %s: %v", currency, err)
			return
		}

		sell, err := strconv.ParseFloat(strings.TrimSpace(cols.Eq(3).Find("h3").Text()), 64)
		if err != nil {
			log.Printf("Ошибка парсинга продажи %s: %v", currency, err)
			return
		}

		change := ""
		if changeElem := cols.Eq(2).Find("sup font"); changeElem.Length() > 0 {
			change = strings.TrimSpace(changeElem.Text())
		}

		rates = append(rates, RulyaRate{
			Currency: currency,
			Buy:      buy,
			Sell:     sell,
			Change:   change,
		})
	})

	if len(rates) == 0 {
		return nil, fmt.Errorf("курсы не найдены")
	}

	return rates, nil
}

func sendMessage(bot *tgbotapi.BotAPI, chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	if _, err := bot.Send(msg); err != nil {
		log.Printf("Ошибка отправки: %v", err)
	}
}

func sendRates(bot *tgbotapi.BotAPI, chatID int64, rates CachedRates) {
	response := fmt.Sprintf("📊 Курсы Rulya Bank (актуально на %s):\n\n",
		rates.LastUpdated.Format("02.01.2006 15:04"))

	for _, rate := range rates.Rates {
		response += fmt.Sprintf("➡ %s: покупка %.2f, продажа %.2f (%s)\n",
			rate.Currency, rate.Buy, rate.Sell, rate.Change)
	}

	sendMessage(bot, chatID, response)
}
