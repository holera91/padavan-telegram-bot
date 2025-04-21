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
	"syscall"

	"github.com/PuerkitoBio/goquery"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Структура для хранения курса валют
type RulyaRate struct {
	Currency string
	Buy      float64
	Sell     float64
	Change   string
}

func main() {
	// Получаем токен бота из переменной окружения
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN environment variable not set")
	}

	// Создаем кастомный HTTP-клиент с отключенной проверкой SSL
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	httpClient := &http.Client{Transport: tr}

	// Создаем бота с кастомным HTTP-клиентом
	bot, err := tgbotapi.NewBotAPIWithClient(botToken, tgbotapi.APIEndpoint, httpClient)
	if err != nil {
		log.Fatal(err)
	}

	bot.Debug = true
	log.Printf("Бот авторизован как %s", bot.Self.UserName)

	// Настраиваем канал обновлений
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	// Канал для обработки сигналов завершения
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Основной цикл обработки сообщений
	for {
		select {
		case update := <-updates:
			if update.Message == nil {
				continue
			}

			log.Printf("[%s] %s", update.Message.From.UserName, update.Message.Text)

			// Обрабатываем только команду /rates
			if update.Message.Command() == "rates" {
				rates, err := fetchRulyaRates()
				if err != nil {
					sendMessage(bot, update.Message.Chat.ID, "❌ Не удалось получить курсы: "+err.Error())
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

// Функция получения курсов валют
func fetchRulyaRates() ([]RulyaRate, error) {
	// Создаем кастомный HTTP-клиент с отключенной проверкой SSL
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

	var rates []RulyaRate
	targetCurrencies := map[string]bool{"USD": true, "EUR": true, "PLZ": true}

	doc.Find("table tr").Each(func(i int, row *goquery.Selection) {
		if i == 0 { // Пропускаем заголовок таблицы
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

		rates = append(rates, RulyaRate{
			Currency: currency,
			Buy:      buy,
			Sell:     sell,
			Change:   change,
		})
	})

	if len(rates) == 0 {
		return nil, fmt.Errorf("не знайдено курсів")
	}

	return rates, nil
}

// Вспомогательная функция для отправки сообщений
func sendMessage(bot *tgbotapi.BotAPI, chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	if _, err := bot.Send(msg); err != nil {
		log.Printf("Помилка відправки: %v", err)
	}
}

// Функция отправки курсов валют
func sendRates(bot *tgbotapi.BotAPI, chatID int64, rates []RulyaRate) {
	response := "📊 Курси Rulya Bank:\n\n"
	for _, rate := range rates {
		response += fmt.Sprintf("➡ %s: купівля %.2f, продаж %.2f (%s)\n",
			rate.Currency, rate.Buy, rate.Sell, rate.Change)
	}
	sendMessage(bot, chatID, response)
}
