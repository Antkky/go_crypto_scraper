package structs

import "encoding/json"

type ExchangeConfig struct {
	Name    string          `json:"name"`
	URI     string          `json:"uri"`
	Streams json.RawMessage `json:"streams"`
	Ping    json.RawMessage `json:"ping,omitempty"`
}

type TickerData struct {
	TimeStamp uint
	Date      uint
	Symbol    string
	BidPrice  float32
	BidSize   float32
	AskPrice  float32
	AskSize   float32
}

type TradeData struct {
	TimeStamp uint
	Date      uint
	Symbol    string
	BidPrice  uint
	BidSize   uint
	AskPrice  uint
	AskSize   uint
}
