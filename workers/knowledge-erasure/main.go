// Trusted local erasure controller. No HTTP route accepts erasure receipts.
package main

import (
	"context"
	"encoding/json"
	"github.com/SMSFlow-ZA/whatomate-addons/processing"
	"io"
	"os"
)

func run() error {
	var configuration struct{ JobRoot, ReceiptRoot string }
	f, e := os.Open("/run/config/erasure.json")
	if e != nil {
		return e
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 65536))
	d.DisallowUnknownFields()
	if e = d.Decode(&configuration); e != nil {
		return e
	}
	var request processing.ErasureRequest
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, 65536))
	decoder.DisallowUnknownFields()
	if e = decoder.Decode(&request); e != nil {
		return e
	}
	if e = decoder.Decode(new(any)); e != io.EOF {
		return io.ErrUnexpectedEOF
	}
	receipt, e := processing.EraseJob(context.Background(), processing.ErasureConfig{JobRoot: configuration.JobRoot, ReceiptRoot: configuration.ReceiptRoot}, request)
	if e != nil {
		return e
	}
	return json.NewEncoder(os.Stdout).Encode(receipt)
}
func main() {
	if run() != nil {
		os.Stderr.WriteString("trusted job erasure failed; no removal confirmation issued\n")
		os.Exit(1)
	}
}
