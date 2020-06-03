package pivx

import (
	"blockbook/bchain"
	"blockbook/bchain/coins/btc"
	"blockbook/bchain/coins/utils"
	"bytes"
	"io"

	"encoding/hex"
	"encoding/json"

	"math/big"
	"github.com/martinboehm/btcd/blockchain"

	"github.com/martinboehm/btcd/wire"
	"github.com/martinboehm/btcutil/chaincfg"
	"github.com/juju/errors"
)

const (
	MainnetMagic wire.BitcoinNet = 0xa3eab581
)

var (
	MainNetParams chaincfg.Params
)

func init() {
	MainNetParams = chaincfg.MainNetParams
	MainNetParams.Net = MainnetMagic
	MainNetParams.PubKeyHashAddrID = []byte{125}
	MainNetParams.ScriptHashAddrID = []byte{18}
	MainNetParams.PrivateKeyID = []byte{140}
}

// BytzParser handle
type BytzParser struct {
	*btc.BitcoinParser
	baseparser *bchain.BaseParser
	BitcoinOutputScriptToAddressesFunc btc.OutputScriptToAddressesFunc
}

// NewBytzParser returns new BytzParser instance
func NewBytzParser(params *chaincfg.Params, c *btc.Configuration) *BytzParser {
	p := &BytzParser{
		BitcoinParser: btc.NewBitcoinParser(params, c),
		baseparser:    &bchain.BaseParser{},
	}
	p.BitcoinOutputScriptToAddressesFunc = p.OutputScriptToAddressesFunc
	p.OutputScriptToAddressesFunc = p.outputScriptToAddresses
	return p
}

// GetChainParams contains network parameters for the main Bytz network
func GetChainParams(chain string) *chaincfg.Params {
	if !chaincfg.IsRegistered(&MainNetParams) {
		err := chaincfg.Register(&MainNetParams)
		if err != nil {
			panic(err)
		}
	}
	return &MainNetParams
}

// ParseBlock parses raw block to our Block struct
func (p *BytzParser) ParseBlock(b []byte) (*bchain.Block, error) {
	r := bytes.NewReader(b)
	w := wire.MsgBlock{}
	h := wire.BlockHeader{}
	err := h.Deserialize(r)
	if err != nil {
		return nil, errors.Annotatef(err, "Deserialize")
	}

  //confirm if this is needed for bytz
	if (h.Version > 1) {
		// Skip past AccumulatorCheckpoint which was added in pivx block version 2 //confirm this
		_, err = r.Seek(32, io.SeekCurrent)
	}

	err = utils.DecodeTransactions(r, 0, wire.WitnessEncoding, &w)
	if err != nil {
		return nil, errors.Annotatef(err, "DecodeTransactions")
	}

	txs := make([]bchain.Tx, len(w.Transactions))
	for ti, t := range w.Transactions {
		txs[ti] = p.TxFromMsgTx(t, false)
	}

	return &bchain.Block{
		BlockHeader: bchain.BlockHeader{
			Size: len(b),
			Time: h.Timestamp.Unix(),
		},
		Txs: txs,
	}, nil
}

// PackTx packs transaction to byte array using protobuf
func (p *BytzParser) PackTx(tx *bchain.Tx, height uint32, blockTime int64) ([]byte, error) {
	return p.baseparser.PackTx(tx, height, blockTime)
}

// UnpackTx unpacks transaction from protobuf byte array
func (p *BytzParser) UnpackTx(buf []byte) (*bchain.Tx, uint32, error) {
	return p.baseparser.UnpackTx(buf)
}

// ParseTx parses byte array containing transaction and returns Tx struct
func (p *BytzParser) ParseTx(b []byte) (*bchain.Tx, error) {
	t := wire.MsgTx{}
	r := bytes.NewReader(b)
	if err := t.Deserialize(r); err != nil {
		return nil, err
	}
	tx := p.TxFromMsgTx(&t, true)
	tx.Hex = hex.EncodeToString(b)
	return &tx, nil
}

// Parses tx and adds handling for OP_ZEROCOINSPEND inputs
func (p *BytzParser) TxFromMsgTx(t *wire.MsgTx, parseAddresses bool) bchain.Tx {
	vin := make([]bchain.Vin, len(t.TxIn))
	for i, in := range t.TxIn {

		// extra check to not confuse Tx with single OP_ZEROCOINSPEND input as a coinbase Tx
		if (!isZeroCoinSpendScript(in.SignatureScript) && blockchain.IsCoinBaseTx(t)) {
			vin[i] = bchain.Vin{
				Coinbase: hex.EncodeToString(in.SignatureScript),
				Sequence: in.Sequence,
			}
			break
		}

		s := bchain.ScriptSig{
			Hex: hex.EncodeToString(in.SignatureScript),
			// missing: Asm,
		}

		txid := in.PreviousOutPoint.Hash.String()

		vin[i] = bchain.Vin{
			Txid:      txid,
			Vout:      in.PreviousOutPoint.Index,
			Sequence:  in.Sequence,
			ScriptSig: s,
		}
	}
	vout := make([]bchain.Vout, len(t.TxOut))
	for i, out := range t.TxOut {
		addrs := []string{}
		if parseAddresses {
			addrs, _, _ = p.OutputScriptToAddressesFunc(out.PkScript)
		}
		s := bchain.ScriptPubKey{
			Hex:       hex.EncodeToString(out.PkScript),
			Addresses: addrs,
			// missing: Asm,
			// missing: Type,
		}
		var vs big.Int
		vs.SetInt64(out.Value)
		vout[i] = bchain.Vout{
			ValueSat:     vs,
			N:            uint32(i),
			ScriptPubKey: s,
		}
	}
	tx := bchain.Tx{
		Txid:     t.TxHash().String(),
		Version:  t.Version,
		LockTime: t.LockTime,
		Vin:      vin,
		Vout:     vout,
		// skip: BlockHash,
		// skip: Confirmations,
		// skip: Time,
		// skip: Blocktime,
	}
	return tx
}

// ParseTxFromJson parses JSON message containing transaction and returns Tx struct
func (p *BytzParser) ParseTxFromJson(msg json.RawMessage) (*bchain.Tx, error) {
	var tx bchain.Tx
	err := json.Unmarshal(msg, &tx)
	if err != nil {
		return nil, err
	}

	for i := range tx.Vout {
		vout := &tx.Vout[i]
		// convert vout.JsonValue to big.Int and clear it, it is only temporary value used for unmarshal
		vout.ValueSat, err = p.AmountToBigInt(vout.JsonValue)
		if err != nil {
			return nil, err
		}
		vout.JsonValue = ""

		if(vout.ScriptPubKey.Addresses == nil) {
			vout.ScriptPubKey.Addresses = []string{}
		}
	}

	return &tx, nil
}


// outputScriptToAddresses converts ScriptPubKey to bitcoin addresses
func (p *BytzParser) outputScriptToAddresses(script []byte) ([]string, bool, error) {
	if (isZeroCoinSpendScript(script) || isZeroCoinMintScript(script)) {
		hexScript := hex.EncodeToString(script)
		anonAddr := "Anonymous " + hexScript
		return []string{anonAddr}, false, nil
	}

	rv, s, _ :=  p.BitcoinOutputScriptToAddressesFunc(script)
	return rv, s, nil
}

func (p *BytzParser) GetAddrDescForUnknownInput(tx *bchain.Tx, input int) bchain.AddressDescriptor {
	if len(tx.Vin) > input {
		scriptHex := tx.Vin[input].ScriptSig.Hex

		if(scriptHex != "") {
			script, _ := hex.DecodeString(scriptHex)
			return script
		}
	}

	s := make([]byte, 10)
	return s
}

// Checks if script is OP_ZEROCOINMINT
func isZeroCoinMintScript(signatureScript []byte) bool {
	OP_ZEROCOINMINT := byte(0xc1)

	if (len(signatureScript) > 1 && signatureScript[0] == OP_ZEROCOINMINT) {
		return true
	}

	return false
}

// Checks if script is OP_ZEROCOINSPEND
func isZeroCoinSpendScript(signatureScript []byte) bool {
	OP_ZEROCOINSPEND := byte(0xc2)

	if (len(signatureScript) >= 100 && signatureScript[0] == OP_ZEROCOINSPEND) {
		return true
	}

	return false
}
