package binance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/Antkky/go_crypto_scraper/utils"
	"github.com/Antkky/go_crypto_scraper/utils/buffer"
	"github.com/gorilla/websocket"
)

// ________Small Helper Functions________

func WrappedCheck(message []byte) (bool, error) {
	var pMessage GlobalMessageStruct

	if err := json.Unmarshal(message, &pMessage); err != nil {
		return false, err
	}

	if pMessage.Data.EventType != "" {
		return true, nil
	}
	if pMessage.EventType != "" {
		return false, nil
	}
	return false, errors.New("unknown message type")
}

func extractEventType(msg GlobalMessageStruct) string {
	if msg.Data.EventType != "" {
		return msg.Data.EventType
	}
	return msg.EventType
}

func processWrapped(wrapped bool, message []byte, bmessage *[]byte) error {
	if wrapped {
		var wrappedMsg struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(message, &wrappedMsg); err != nil {
			return err
		}
		*bmessage = wrappedMsg.Data
	} else {
		*bmessage = message
	}
	return nil
}

// ________Main Functions________

// InitializeStreams()
//
// Inputs:
//
//	conn        : *websocket.Conn
//	exchange    : utils.ExchangeConfig
//	dataBuffers : []*utils.DataBuffer
//
// Outputs:
//
//	No Outputs
//
// Description:
//
//	Initializes the streams by subscribing to the streams and creating the data buffers
func InitializeStreams(conn *websocket.Conn, exchange utils.ExchangeConfig, dataBuffers *map[string]*buffer.DataBuffer, logger *log.Logger) error {
	*dataBuffers = make(map[string]*buffer.DataBuffer)

	for _, stream := range exchange.Streams {
		bMessage, err := json.Marshal(stream.Message)
		filename := fmt.Sprintf("%s_%s_%s.csv", strings.ReplaceAll(exchange.Name, " ", ""), stream.Symbol, stream.Type)
		bufferCode := fmt.Sprintf("%s:%s@%s", stream.Symbol, stream.Type, strings.ReplaceAll(exchange.Name, " ", ""))
		filePath := fmt.Sprintf("data/%s/%s", strings.ReplaceAll(exchange.Name, " ", ""), stream.Symbol)
		(*dataBuffers)[bufferCode] = buffer.NewDataBuffer(stream.Type, stream.Market, bufferCode, 50, filename, filePath)

		if err != nil {
			logger.Printf("❌ Error marshalling subscribe message %v: %s", stream, err)
			return err
		}

		if err := conn.WriteMessage(websocket.TextMessage, bMessage); err != nil {
			logger.Printf("❌ Error subscribing to stream %v: %s", stream, err)
			return err
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil
}

// HandleConnection()
//
// Inputs:
//
//	conn     : *websocket.Conn
//	exchange : utils.ExchangeConfig
//
// Outputs:
//
//	No Outputs
//
// Description:
//
//	goroutine that subscribes and launches 2 goroutines to listen for messages and handle them
func HandleConnection(conn *websocket.Conn, exchange utils.ExchangeConfig, logger *log.Logger) {
	if conn == nil {
		logger.Println("❌ Connection is nil, exiting HandleConnection.")
		return
	}

	// isEmpty checks if the given data is empty

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	defer signal.Stop(interrupt)

	messageQueue := make(chan []byte, 500)
	done := make(chan struct{})

	dataBuffers := make(map[string]*buffer.DataBuffer)
	if err := InitializeStreams(conn, exchange, &dataBuffers, logger); err != nil {
		return
	}

	go ConsumeMessages(messageQueue, exchange, done, dataBuffers, logger)
	go ReceiveMessages(conn, messageQueue, done, exchange, logger)

	<-interrupt
	logger.Println("Interrupt received, closing connection...")

	CloseConnection(conn, exchange.Name, logger)
}

// ProcessMessageType()
//
// Inputs:
//
//	message    : []byte
//	tickerData : *utils.TickerDataStruct
//	tradeData  : *utils.TradeDataStruct
//
// Outputs:
//
//	error
//
// Description:
//
//	basically routes the data to the correct processing function
func ProcessMessage(message []byte, tickerDataP *[]utils.TickerDataStruct, tradeData *[]utils.TradeDataStruct) (int, error) {
	if bytes.Equal(message, []byte(`{"result":null,"id":5}`)) {
		return 5, nil
	}

	var pMessage GlobalMessageStruct
	wrapped, err := WrappedCheck(message)
	if err != nil {
		return 0, err
	}

	var bmessage []byte
	if err := processWrapped(wrapped, message, &bmessage); err != nil {
		return 0, err
	}

	if err := json.Unmarshal(bmessage, &pMessage); err != nil {
		return 0, err
	}

	switch {
	case extractEventType(pMessage) == "24hrTicker":
		var tickerMsg TickerData
		if err := json.Unmarshal(bmessage, &tickerMsg); err != nil {
			return 1, err
		}
		*tickerDataP = []utils.TickerDataStruct{{
			TimeStamp: uint64(tickerMsg.EventTime),
			Symbol:    tickerMsg.Symbol,
			BidPrice:  string(tickerMsg.BidPrice),
			BidSize:   string(tickerMsg.BidSize),
			AskPrice:  string(tickerMsg.AskPrice),
			AskSize:   string(tickerMsg.AskSize),
		}}
		return 1, nil

	case extractEventType(pMessage) == "trade":
		var tradeMsg TradeData
		if err := json.Unmarshal(bmessage, &tradeMsg); err != nil {
			return 2, err
		}
		*tradeData = []utils.TradeDataStruct{{
			TimeStamp: uint64(tradeMsg.EventTime),
			Symbol:    tradeMsg.Symbol,
			Price:     tradeMsg.Price,
			Quantity:  tradeMsg.Quantity,
			Bid_MM:    tradeMsg.IsMaker,
		}}
		return 2, nil

	default:
		return 0, fmt.Errorf("unknown message type: %s", message)
	}
}

// ConsumeMessages()
//
// Inputs:
//
//	messageQueue  : chan []byte
//	done          : chan struct{}
//	exchange      : utils.ExchangeConfig
//
// Outputs:
//
//	No Outputs
//
// Description:
//
//	Processes incoming messages and adds them to the appropriate data buffer.
//	This function performs constant time lookups for the buffer associated with each message.
func ConsumeMessages(messageQueue chan []byte, exchange utils.ExchangeConfig, done chan struct{}, buffers map[string]*buffer.DataBuffer, logger *log.Logger) {
	defer close(done)
	normalizedExchangeName := strings.ReplaceAll(exchange.Name, " ", "")

	for message := range messageQueue {
		bufferCode := ""
		tickerData := []utils.TickerDataStruct{}
		tradeData := []utils.TradeDataStruct{}

		dataType, err := ProcessMessage(message, &tickerData, &tradeData)
		if err != nil {
			logger.Printf("❌ Error processing message: %v", err)
			continue
		}

		switch dataType {
		case 0:
			logger.Println("❌ Unknown message type, skipping message")
			continue
		case 1:
			if tickerData[0].Symbol != "" {
				bufferCode = fmt.Sprintf("%s:ticker@%s", tickerData[0].Symbol, normalizedExchangeName)
			}
		case 2:
			if tradeData[0].Symbol != "" {
				bufferCode = fmt.Sprintf("%s:trade@%s", tradeData[0].Symbol, normalizedExchangeName)
			}
		case 5:
			logger.Println("✅ Subscribe Success")
		}

		if bufferCode != "" {
			if buffer, exists := buffers[bufferCode]; exists {
				if dataType == 1 {
					if err := buffer.AddData(tickerData); err != nil {
						logger.Println("❌ Error adding data to buffer: ", err)
						return
					}
				} else {
					if err := buffer.AddData(tradeData); err != nil {
						logger.Println("❌ Error adding data to buffer: ", err)
						return
					}
				}
			} else {
				logger.Printf("❌ No buffer found for ID: %s", bufferCode)
			}
		}
	}
}

// ReceiveMessages()
//
// Inputs:
//
//	conn          : *websocket.Conn
//	messageQueue  : chan []byte
//	done          : chan struct{}
//	exchange      : utils.ExchangeConfig
//
// Outputs:
//
//	No Outputs
//
// Description:
//
//	Reads messages from the WebSocket connection and sends them to the messageQueue channel.
func ReceiveMessages(conn *websocket.Conn, messageQueue chan []byte, done chan struct{}, exchange utils.ExchangeConfig, logger *log.Logger) {
	defer close(done)

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			logger.Printf("❌ Error reading message from %s: %v", exchange.Name, err)
			return
		}

		select {
		case messageQueue <- message:
			//bruh
		case <-time.After(time.Millisecond * 100):
			logger.Println("Producer slowed down")
		default:
			logger.Printf("❌ Message queue full, dropping message for %s", exchange.Name)
		}
	}
}

// CloseConnection()
//
// Inputs:
//
//	conn         : *websocket.conn
//	exchangeName : string
//
// Outputs:
//
//	No Outputs
//
// Description:
//
//	Gracefully close the connection by sending a closure message and gracefully close connection
func CloseConnection(conn *websocket.Conn, exchangeName string, logger *log.Logger) {
	if err := conn.Close(); err != nil {
		logger.Printf("❌ Error closing connection for %s: %v", exchangeName, err)
	} else {
		logger.Printf("Connection for %s closed gracefully", exchangeName)
	}
}
