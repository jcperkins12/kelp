package backend

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/stellar/go/clients/horizon"
	"github.com/stellar/go/clients/horizonclient"
	hProtocol "github.com/stellar/go/protocols/horizon"
	"github.com/stellar/go/support/config"
	"github.com/stellar/kelp/gui/model2"
	"github.com/stellar/kelp/model"
	"github.com/stellar/kelp/query"
	"github.com/stellar/kelp/support/kelpos"
	"github.com/stellar/kelp/support/utils"
	"github.com/stellar/kelp/trader"
)

const buysell = "buysell"

func (s *APIServer) getBotInfo(w http.ResponseWriter, r *http.Request) {
	botName, e := s.parseBotName(r)
	if e != nil {
		s.writeError(w, fmt.Sprintf("error parsing bot name in getBotInfo: %s\n", e))
		return
	}

	// s.runGetBotInfoViaIPC(w, botName)
	s.runGetBotInfoDirect(w, botName)
}

func (s *APIServer) runGetBotInfoViaIPC(w http.ResponseWriter, botName string) {
	p, exists := s.kos.GetProcess(botName)
	if !exists {
		log.Printf("kelp bot process with name '%s' does not exist; processes available: %v\n", botName, s.kos.RegisteredProcesses())
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("{}"))
		return
	}

	log.Printf("getBotInfo is making IPC request for botName: %s\n", botName)
	p.PipeIn.Write([]byte("getBotInfo\n"))
	scanner := bufio.NewScanner(p.PipeOut)
	output := ""
	for scanner.Scan() {
		text := scanner.Text()
		if strings.Contains(text, utils.IPCBoundary) {
			break
		}
		output += text
	}
	var buf bytes.Buffer
	e := json.Indent(&buf, []byte(output), "", "  ")
	if e != nil {
		log.Printf("cannot indent json response (error=%s), json_response: %s\n", e, output)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("{}"))
		return
	}
	log.Printf("getBotInfo returned IPC response for botName '%s': %s\n", botName, buf.String())

	w.WriteHeader(http.StatusOK)
	w.Write(buf.Bytes())
}

func (s *APIServer) runGetBotInfoDirect(w http.ResponseWriter, botName string) {
	log.Printf("getBotInfo is invoking logic directly for botName: %s\n", botName)

	botState, e := s.doGetBotState(botName)
	if e != nil {
		s.writeErrorJson(w, fmt.Sprintf("cannot read bot state for bot '%s': %s\n", botName, e))
		return
	}
	if botState == kelpos.BotStateInitializing {
		log.Printf("bot state is initializing for bot '%s'\n", botName)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{}"))
		return
	}

	filenamePair := model2.GetBotFilenames(botName, buysell)
	traderFilePath := fmt.Sprintf("%s/%s", s.configsDir, filenamePair.Trader)
	var botConfig trader.BotConfig
	e = config.Read(traderFilePath, &botConfig)
	if e != nil {
		s.writeErrorJson(w, fmt.Sprintf("cannot read bot config at path '%s': %s\n", traderFilePath, e))
		return
	}
	e = botConfig.Init()
	if e != nil {
		s.writeErrorJson(w, fmt.Sprintf("cannot init bot config at path '%s': %s\n", traderFilePath, e))
		return
	}

	assetBase := botConfig.AssetBase()
	assetQuote := botConfig.AssetQuote()
	tradingPair := &model.TradingPair{
		Base:  model.Asset(utils.Asset2CodeString(assetBase)),
		Quote: model.Asset(utils.Asset2CodeString(assetQuote)),
	}
	account, e := s.apiTestNet.AccountDetail(horizonclient.AccountRequest{AccountID: botConfig.TradingAccount()})
	if e != nil {
		s.writeErrorJson(w, fmt.Sprintf("cannot get account data for account '%s' for botName '%s': %s\n", botConfig.TradingAccount(), botName, e))
		return
	}
	var balanceBase float64
	if assetBase == utils.NativeAsset {
		balanceBase, e = getNativeBalance(account)
		if e != nil {
			s.writeErrorJson(w, fmt.Sprintf("error getting native balanceBase for account '%s' for botName '%s': %s\n", botConfig.TradingAccount(), botName, e))
			return
		}
	} else {
		balanceBase, e = getCreditBalance(account, assetBase)
		if e != nil {
			s.writeErrorJson(w, fmt.Sprintf("error getting credit balanceBase for account '%s' for botName '%s': %s\n", botConfig.TradingAccount(), botName, e))
			return
		}
	}
	var balanceQuote float64
	if assetQuote == utils.NativeAsset {
		balanceQuote, e = getNativeBalance(account)
		if e != nil {
			s.writeErrorJson(w, fmt.Sprintf("error getting native balanceQuote for account '%s' for botName '%s': %s\n", botConfig.TradingAccount(), botName, e))
			return
		}
	} else {
		balanceQuote, e = getCreditBalance(account, assetQuote)
		if e != nil {
			s.writeErrorJson(w, fmt.Sprintf("error getting credit balanceQuote for account '%s' for botName '%s': %s\n", botConfig.TradingAccount(), botName, e))
			return
		}
	}

	offers, e := utils.LoadAllOffers(account.AccountID, s.apiTestNet)
	if e != nil {
		s.writeErrorJson(w, fmt.Sprintf("error getting offers for account '%s' for botName '%s': %s\n", botConfig.TradingAccount(), botName, e))
		return
	}
	sellingAOffers, buyingAOffers := utils.FilterOffers(offers, assetBase, assetQuote)
	numBids := len(buyingAOffers)
	numAsks := len(sellingAOffers)

	obs, e := s.apiTestNet.OrderBook(horizonclient.OrderBookRequest{
		SellingAssetType:   horizonclient.AssetType(assetBase.Type),
		SellingAssetCode:   assetBase.Code,
		SellingAssetIssuer: assetBase.Issuer,
		BuyingAssetType:    horizonclient.AssetType(assetQuote.Type),
		BuyingAssetCode:    assetQuote.Code,
		BuyingAssetIssuer:  assetQuote.Issuer,
		Limit:              1,
	})
	if e != nil {
		s.writeErrorJson(w, fmt.Sprintf("error getting orderbook for assets (base=%v, quote=%v) for botName '%s': %s\n", assetBase, assetQuote, botName, e))
		return
	}
	spread := -1.0
	spreadPct := -1.0
	if len(obs.Asks) > 0 && len(obs.Bids) > 0 {
		topAsk := float64(obs.Asks[0].PriceR.N) / float64(obs.Asks[0].PriceR.D)
		topBid := float64(obs.Bids[0].PriceR.N) / float64(obs.Bids[0].PriceR.D)

		spread = topAsk - topBid
		midPrice := (topAsk + topBid) / 2
		spreadPct = spread / midPrice
	}

	bi := query.BotInfo{
		LastUpdated:   time.Now().Format("1/_2/2006 15:04:05"),
		Strategy:      buysell,
		IsTestnet:     strings.Contains(botConfig.HorizonURL, "test"),
		TradingPair:   tradingPair,
		AssetBase:     assetBase,
		AssetQuote:    assetQuote,
		BalanceBase:   balanceBase,
		BalanceQuote:  balanceQuote,
		NumBids:       numBids,
		NumAsks:       numAsks,
		SpreadValue:   model.NumberFromFloat(spread, 8).AsFloat(),
		SpreadPercent: model.NumberFromFloat(spreadPct, 8).AsFloat(),
	}

	marshalledJson, e := json.MarshalIndent(bi, "", "  ")
	if e != nil {
		log.Printf("cannot marshall to json response (error=%s), BotInfo: %+v\n", e, bi)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("{}"))
		return
	}
	marshalledJsonString := string(marshalledJson)
	log.Printf("getBotInfo returned direct response for botName '%s': %s\n", botName, marshalledJsonString)

	w.WriteHeader(http.StatusOK)
	w.Write(marshalledJson)
}

func getNativeBalance(account hProtocol.Account) (float64, error) {
	balanceString, e := account.GetNativeBalance()
	if e != nil {
		return 0.0, fmt.Errorf("cannot get native balance: %s\n", e)
	}

	balance, e := strconv.ParseFloat(balanceString, 64)
	if e != nil {
		return 0.0, fmt.Errorf("cannot parse native balance: %s (string value = %s)\n", e, balanceString)
	}

	return balance, nil
}

func getCreditBalance(account hProtocol.Account, asset horizon.Asset) (float64, error) {
	balanceString := account.GetCreditBalance(asset.Code, asset.Issuer)
	balance, e := strconv.ParseFloat(balanceString, 64)
	if e != nil {
		return 0.0, fmt.Errorf("cannot parse credit asset balance (%s:%s): %s (string value = %s)\n", asset.Code, asset.Issuer, e, balanceString)
	}

	return balance, nil
}
