package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// ANSI color codes
const (
	bold   = "\033[1m"
	white  = "\033[97m"
	reset  = "\033[0m"
	green  = "\033[32m"
	blue   = "\033[34m"
	red    = "\033[31m"
	yellow = "\033[33m"
)

func isBinary(data []byte) bool {
	for _, b := range data {
		if b < 32 || b > 126 {
			if b != '\n' && b != '\r' && b != '\t' {
				return true
			}
		}
	}
	return false
}

func previewRawValue(val []byte) string {
	if len(val) > 10 {
		return fmt.Sprintf("%q...", val[:10])
	}
	return fmt.Sprintf("%q", val)
}

func previewUTF8(val []byte) string {
	if !utf8.Valid(val) {
		return "[non-utf8]"
	}
	runes := []rune(string(val))
	if len(runes) > 10 {
		return fmt.Sprintf("%q...", string(runes[:10]))
	}
	return fmt.Sprintf("%q", string(runes))
}

func loadTLSConfig(cacert, cert, key string) (*tls.Config, error) {
	certPair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		return nil, fmt.Errorf("load cert/key failed: %w", err)
	}
	caData, err := ioutil.ReadFile(cacert)
	if err != nil {
		return nil, fmt.Errorf("read CA failed: %w", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caData) {
		return nil, fmt.Errorf("CA parse failed")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{certPair},
		RootCAs:      caPool,
	}, nil
}

func main() {
	endpoints := flag.String("endpoints", os.Getenv("ETCDCTL_ENDPOINTS"), "Comma-separated list of etcd endpoints")
	cacert := flag.String("cacert", os.Getenv("ETCDCTL_CACERT"), "Path to trusted CA file")
	cert := flag.String("cert", os.Getenv("ETCDCTL_CERT"), "Path to client certificate")
	key := flag.String("key", os.Getenv("ETCDCTL_KEY"), "Path to client private key")
	hexPrefix := flag.String("prefix", "", "Hexadecimal prefix of keys to scan")
	timeout := flag.Duration("timeout", 5*time.Second, "Request timeout")
	debug := flag.Bool("debug", false, "Print UTF-8 keys and values")
	remove := flag.Bool("remove", false, "Delete binary keys")
	dryRun := flag.Bool("dry", false, "Dry-run mode (simulates deletion)")
	flag.Parse()

	if *endpoints == "" {
		log.Fatal("Missing etcd endpoints (--endpoints or $ETCDCTL_ENDPOINTS)")
	}

	if *dryRun && *remove {
		fmt.Println("Note: --dry overrides --remove; performing dry-run only")
	}

	// Determine effective modes
	modeDryRun := *dryRun
	modeDelete := *remove && !*dryRun

	var tlsConfig *tls.Config
	if *cacert != "" && *cert != "" && *key != "" {
		var err error
		tlsConfig, err = loadTLSConfig(*cacert, *cert, *key)
		if err != nil {
			log.Fatalf("TLS config error: %v", err)
		}
	}

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   strings.Split(*endpoints, ","),
		DialTimeout: *timeout,
		TLS:         tlsConfig,
	})
	if err != nil {
		log.Fatalf("etcd client error: %v", err)
	}
	defer cli.Close()

	var prefix []byte
	if *hexPrefix != "" {
		prefix, err = hex.DecodeString(*hexPrefix)
		if err != nil {
			log.Fatalf("Failed to decode hex prefix %q: %v", *hexPrefix, err)
		}
	} else {
		prefix = []byte("")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	resp, err := cli.Get(ctx, string(prefix), clientv3.WithPrefix())
	cancel()
	if err != nil {
		log.Fatalf("Failed to get keys from etcd: %v", err)
	}

	totalCount := len(resp.Kvs)
	binaryCount := 0
	utf8Count := 0
	deletedCount := 0
	dryRunCount := 0

	for _, kv := range resp.Kvs {
		key := kv.Key
		val := kv.Value

		if isBinary(key) || !utf8.Valid(key) {
			fmt.Printf("%sBINARY KEY%s: hex=%s, raw=%q, value=%s\n",
				green, reset,
				hex.EncodeToString(key), key, previewRawValue(val))
			binaryCount++

			if modeDryRun {
				fmt.Printf("Delete key: %q (dry-run)\n", string(key))
				dryRunCount++
			} else if modeDelete {
				ctx, cancel := context.WithTimeout(context.Background(), *timeout)
				_, err := cli.Delete(ctx, string(key))
				cancel()
				if err != nil {
					fmt.Printf("Failed to delete key: %q, error: %v\n", key, err)
				} else {
					fmt.Printf("%sDeleted key%s: %q\n",
							red, reset, key)
					deletedCount++
				}
			}
		} else {
			utf8Count++
			if *debug {
				fmt.Printf("%sUTF8 KEY%s: %q, value=%s\n",
					blue, reset,
					key, previewUTF8(val))
			}
		}
	}

	// Print Mode
	if modeDryRun {
		fmt.Println("\nMode: dry-run")
	} else if modeDelete {
		fmt.Println("\nMode: delete")
	}

	// Summary
	fmt.Printf("\n%s%sSUMMARY%s:\n", bold, white, reset)
	fmt.Printf("  %sBinary keys%s:  %d\n", green, reset, binaryCount)
	fmt.Printf("  %sUTF-8 keys%s:   %d\n", blue, reset, utf8Count)
	fmt.Printf("  Total keys:   %d\n", totalCount)
	if modeDryRun {
		fmt.Printf("  %sDry-run%s:      %d keys would be deleted\n", yellow, reset, dryRunCount)
	}
	if modeDelete {
		fmt.Printf("  %sDeleted%s:      %d keys\n", red, reset, deletedCount)
	}
}
