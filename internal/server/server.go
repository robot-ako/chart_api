package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"

	"github.com/SystemAlgoFund/chart_api/internal/config"
	"github.com/SystemAlgoFund/chart_api/internal/proto"
	"github.com/jmoiron/sqlx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

type ChartAPIServer struct {
	proto.UnimplementedChartAPIServer
	DB *sqlx.DB
}

func (s *ChartAPIServer) GetAlgos(ctx context.Context, req *proto.GetAlgosRequest) (*proto.GetAlgosResponse, error) {
	var tables []string
	query := `
        SELECT tablename
        FROM pg_tables
        WHERE schemaname = 'public' AND tablename LIKE 'algo_%'
    `
	err := s.DB.Select(&tables, query)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch algorithm tables: %v", err)
	}
	return &proto.GetAlgosResponse{Algos: tables}, nil
}

type IndicatorParam struct {
	Type  string             `json:"type"`
	Param map[string]float64 `json:"param"`
}

type StreamData struct {
	ID             int              `db:"id"`
	User           string           `db:"user"`
	Coin           string           `db:"coin"`
	CoinSecond     string           `db:"coin_second"`
	Timeframe      string           `db:"timeframe"`
	Algo           string           `db:"algo"`
	PriceDecimal   int              `db:"price_decimal"`
	SizeDecimal    int              `db:"size_decimal"`
	IndicatorParam []IndicatorParam `db:"indicator_param"`
}

func (s *ChartAPIServer) GetStreamsByAlgo(ctx context.Context, req *proto.GetStreamsByAlgoRequest) (*proto.GetStreamsByAlgoResponse, error) {
	var streams []StreamData
	query := `
        SELECT id, user, coin, coin_second, timeframe, algo, price_decimal, size_decimal, indicator_param
        FROM stream
        WHERE algo = $1
    `
	rows, err := s.DB.Queryx(query, req.Algo)
	if err != nil {
		return nil, fmt.Errorf("failed to query streams: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var stream StreamData
		var indicatorParamJSON string
		err := rows.Scan(
			&stream.ID,
			&stream.User,
			&stream.Coin,
			&stream.CoinSecond,
			&stream.Timeframe,
			&stream.Algo,
			&stream.PriceDecimal,
			&stream.SizeDecimal,
			&indicatorParamJSON,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan stream data: %v", err)
		}

		// Десериализация indicator_param из JSON
		var rawIndicatorParams []map[string]interface{}
		if err := json.Unmarshal([]byte(indicatorParamJSON), &rawIndicatorParams); err != nil {
			return nil, fmt.Errorf("failed to unmarshal indicator_param: %v", err)
		}

		// Преобразование map[string]interface{} в map[string]float64
		var indicatorParams []IndicatorParam
		for _, rawParam := range rawIndicatorParams {
			param := make(map[string]float64)
			for key, value := range rawParam["param"].(map[string]interface{}) {
				switch v := value.(type) {
				case float64:
					param[key] = v
				case string:
					// Попытка преобразовать строку в float64
					floatValue, err := strconv.ParseFloat(v, 64)
					if err != nil {
						// Пропустить значение, если оно не может быть преобразовано
						continue
					}
					param[key] = floatValue
				default:
					// Пропустить значение, если оно не является числом или строкой
					continue
				}
			}
			indicatorParams = append(indicatorParams, IndicatorParam{
				Type:  rawParam["type"].(string),
				Param: param,
			})
		}
		stream.IndicatorParam = indicatorParams

		streams = append(streams, stream)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating over rows: %v", err)
	}

	// Преобразование StreamData в proto.StreamData
	var protoStreams []*proto.StreamData
	for _, stream := range streams {
		protoStreams = append(protoStreams, &proto.StreamData{
			Id:             int32(stream.ID),
			User:           stream.User,
			Coin:           stream.Coin,
			CoinSecond:     stream.CoinSecond,
			Timeframe:      stream.Timeframe,
			Algo:           stream.Algo,
			PriceDecimal:   int32(stream.PriceDecimal),
			SizeDecimal:    int32(stream.SizeDecimal),
			IndicatorParam: convertIndicatorParams(stream.IndicatorParam),
		})
	}

	return &proto.GetStreamsByAlgoResponse{Streams: protoStreams}, nil
}

// Вспомогательная функция для преобразования IndicatorParam в proto.IndicatorParam
func convertIndicatorParams(params []IndicatorParam) []*proto.IndicatorParam {
	var protoParams []*proto.IndicatorParam
	for _, param := range params {
		protoParams = append(protoParams, &proto.IndicatorParam{
			Type:  param.Type,
			Param: param.Param,
		})
	}
	return protoParams
}

type StreamInfo struct {
	User       string `db:"user"`
	Algo       string `db:"algo"`
	Coin       string `db:"coin"`
	CoinSecond string `db:"coin_second"`
	Timeframe  string `db:"timeframe"`
}

type DataPoint struct {
	ID        int     `db:"id"`
	Time      string  `db:"time"`
	Timestamp string  `db:"timestamp"`
	Open      float64 `db:"open"`
	High      float64 `db:"high"`
	Low       float64 `db:"low"`
	Close     float64 `db:"close"`
	Volume    float64 `db:"volume"`
}

type Indicator struct {
	Type   string                   `json:"type"`
	Name   string                   `json:"name"`
	Param  map[string]float64       `json:"param"`
	Volume []map[string]interface{} `json:"volume"`
}

func (s *ChartAPIServer) GetData(ctx context.Context, req *proto.GetDataRequest) (*proto.GetDataResponse, error) {
	// Получаем данные из таблицы streams
	var streamInfo StreamInfo
	queryStream := `
        SELECT coin, coin_second, timeframe
        FROM stream
        WHERE id = $1
    `
	err := s.DB.Get(&streamInfo, queryStream, req.Id)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch stream info: %v", err)
	}

	// Формируем название таблицы (например, btc_usdt_1m)
	tableName := fmt.Sprintf("%s_%s_%s", strings.ToLower(streamInfo.Coin), strings.ToLower(streamInfo.CoinSecond), strings.ToLower(streamInfo.Timeframe))

	// Устанавливаем значение по умолчанию для from_timestamp
	fromTimestamp := req.FromTimestamp
	if fromTimestamp == 0 {
		fromTimestamp = 0 // Минимальное Unix-время (1 января 1970 года)
	}

	// Получаем данные из таблицы с данными
	var dataPoints []DataPoint
	queryData := fmt.Sprintf(`
        SELECT id, time, timestamp, open, high, low, close, volume
        FROM %s
        WHERE timestamp >= $1
        ORDER BY timestamp
        LIMIT $2
    `, tableName)
	err = s.DB.Select(&dataPoints, queryData, fromTimestamp, req.Limit)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch data points: %v", err)
	}

	// Формируем данные для indicators
	indicators := []*proto.Indicator{
		{
			Type: "t3",
			Name: "test",
			Param: map[string]float64{
				"slope":  50,
				"factor": 15,
			},
			Volume: []*proto.Volume{
				{
					Params: map[string]float64{
						"side":        1,
						"up_1":        200,
						"up_2":        200,
						"center":      200,
						"down_1":      200,
						"entry_price": 12345.123,
					},
				},
			},
		},
	}

	// Преобразование DataPoint в proto.DataPoint
	var protoDataPoints []*proto.DataPoint
	for _, point := range dataPoints {
		protoDataPoints = append(protoDataPoints, &proto.DataPoint{
			Id:         int32(point.ID),
			Time:       point.Time,
			Timestamp:  point.Timestamp,
			Open:       point.Open,
			High:       point.High,
			Low:        point.Low,
			Close:      point.Close,
			Volume:     point.Volume,
			Indicators: indicators,
		})
	}

	return &proto.GetDataResponse{
		Data: protoDataPoints,
	}, nil
}

type LogEntry struct {
	Time               string  `db:"time"`
	Timestamp          string  `db:"timestamp"`
	Algo               string  `db:"algo"`
	Timeframe          string  `db:"timeframe"`
	BlockId            int     `db:"block_id"`
	BlockName          string  `db:"block_name"`
	IndicatorParam     string  `db:"indicator_param"`
	Vars               string  `db:"vars"`
	Close              float64 `db:"close"`
	Balance            float64 `db:"balance"`
	Equity             float64 `db:"equity"`
	Money              float64 `db:"money"`
	Side               string  `db:"side"`
	OrderLeverageLong  float64 `db:"order_leverage_long"`
	LeverageLong       float64 `db:"leverage_long"`
	OrderSizeLong      float64 `db:"order_size_long"`
	OrderPriceLong     float64 `db:"order_price_long"`
	OrderUsdLong       float64 `db:"order_usd_long"`
	OrderTypeLong      string  `db:"order_type_long"`
	PositionSizeLong   float64 `db:"position_size_long"`
	PositionPriceLong  float64 `db:"position_price_long"`
	PositionUsdLong    float64 `db:"position_usd_long"`
	PnlLong            float64 `db:"pnl_long"`
	FeeLong            float64 `db:"fee_long"`
	FundingLong        float64 `db:"funding_long"`
	RplLong            float64 `db:"rpl_long"`
	OrderLeverageShort float64 `db:"order_leverage_short"`
	LeverageShort      float64 `db:"leverage_short"`
	OrderSizeShort     float64 `db:"order_size_short"`
	OrderPriceShort    float64 `db:"order_price_short"`
	OrderUsdShort      float64 `db:"order_usd_short"`
	OrderTypeShort     string  `db:"order_type_short"`
	PositionUsdShort   float64 `db:"position_usd_short"`
	PositionSizeShort  float64 `db:"position_size_short"`
	PositionPriceShort float64 `db:"position_price_short"`
	PnlShort           float64 `db:"pnl_short"`
	FeeShort           float64 `db:"fee_short"`
	FundingShort       float64 `db:"funding_short"`
	RplShort           float64 `db:"rpl_short"`
	User               string  `db:"user"`
	Mode               string  `db:"mode"`
	ModeTime           string  `db:"mode_time"`
	Coin               string  `db:"coin"`
	CoinSecond         string  `db:"coin_second"`
}

func (s *ChartAPIServer) GetLogs(ctx context.Context, req *proto.GetLogsRequest) (*proto.GetLogsResponse, error) {
	// Получаем данные из таблицы streams
	var streamInfo StreamInfo
	queryStream := `
        SELECT user, algo, coin, coin_second, timeframe
        FROM stream
        WHERE id = $1
    `
	err := s.DB.Get(&streamInfo, queryStream, req.Id)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch stream info: %v", err)
	}

	// Формируем SQL-запрос для таблицы log
	queryLog := `
        SELECT time, timestamp, algo, timeframe,
               block_id, block_name, indicator_param, vars, close, balance, equity, money, side,
               order_leverage_long, leverage_long, order_size_long, order_price_long, order_usd_long, order_type_long,
               position_size_long, position_price_long, position_usd_long, pnl_long, fee_long, funding_long, rpl_long,
               order_leverage_short, leverage_short, order_size_short, order_price_short, order_usd_short, order_type_short,
               position_usd_short, position_size_short, position_price_short, pnl_short, fee_short, funding_short, rpl_short,
               user, mode, mode_time, coin, coin_second
        FROM log
        WHERE user = $1 AND algo = $2 AND coin = $3 AND coin_second = $4 AND timeframe = $5
          AND timestamp >= $6
    `
	// Добавляем условие для orders_only
	if req.OrdersOnly {
		queryLog += " AND order_leverage_long > 0"
	}
	// Добавляем LIMIT
	queryLog += " ORDER BY timestamp LIMIT $7"

	// Получаем данные из таблицы log
	var logs []LogEntry
	err = s.DB.Select(&logs, queryLog,
		streamInfo.User, streamInfo.Algo, streamInfo.Coin, streamInfo.CoinSecond, streamInfo.Timeframe,
		req.FromTimestamp, req.Limit)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch logs: %v", err)
	}

	// Преобразование LogEntry в proto.LogEntry
	var protoLogs []*proto.LogEntry
	for _, log := range logs {
		protoLogs = append(protoLogs, &proto.LogEntry{
			Time:               log.Time,
			Timestamp:          log.Timestamp,
			Algo:               log.Algo,
			Timeframe:          log.Timeframe,
			BlockId:            int32(log.BlockId),
			BlockName:          log.BlockName,
			IndicatorParam:     log.IndicatorParam,
			Vars:               log.Vars,
			Close:              log.Close,
			Balance:            log.Balance,
			Equity:             log.Equity,
			Money:              log.Money,
			Side:               log.Side,
			OrderLeverageLong:  log.OrderLeverageLong,
			LeverageLong:       log.LeverageLong,
			OrderSizeLong:      log.OrderSizeLong,
			OrderPriceLong:     log.OrderPriceLong,
			OrderUsdLong:       log.OrderUsdLong,
			OrderTypeLong:      log.OrderTypeLong,
			PositionSizeLong:   log.PositionSizeLong,
			PositionPriceLong:  log.PositionPriceLong,
			PositionUsdLong:    log.PositionUsdLong,
			PnlLong:            log.PnlLong,
			FeeLong:            log.FeeLong,
			FundingLong:        log.FundingLong,
			RplLong:            log.RplLong,
			OrderLeverageShort: log.OrderLeverageShort,
			LeverageShort:      log.LeverageShort,
			OrderSizeShort:     log.OrderSizeShort,
			OrderPriceShort:    log.OrderPriceShort,
			OrderUsdShort:      log.OrderUsdShort,
			OrderTypeShort:     log.OrderTypeShort,
			PositionUsdShort:   log.PositionUsdShort,
			PositionSizeShort:  log.PositionSizeShort,
			PositionPriceShort: log.PositionPriceShort,
			PnlShort:           log.PnlShort,
			FeeShort:           log.FeeShort,
			FundingShort:       log.FundingShort,
			RplShort:           log.RplShort,
			User:               log.User,
			Mode:               log.Mode,
			ModeTime:           log.ModeTime,
			Coin:               log.Coin,
			CoinSecond:         log.CoinSecond,
		})
	}

	return &proto.GetLogsResponse{
		Logs: protoLogs,
	}, nil
}

func StartGRPCServer(cfg *config.Config, db *sqlx.DB) {
	addr := fmt.Sprintf("%s:%s", cfg.GRPC.Server.Host, cfg.GRPC.Server.Port)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}
	s := grpc.NewServer()
	proto.RegisterChartAPIServer(s, &ChartAPIServer{DB: db})
	reflection.Register(s)
	log.Printf("Starting gRPC server on %s\n", addr)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
