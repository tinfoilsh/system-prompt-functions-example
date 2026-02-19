package main

import (
	"io"
	"log"
	"net/http"
	"os"
)

const (
	// Header containing the enclave URL that the client verified
	enclaveURLHeader = "X-Tinfoil-Enclave-Url"

	// Tinfoil usage metrics headers
	usageMetricsRequestHeader  = "X-Tinfoil-Request-Usage-Metrics"
	usageMetricsResponseHeader = "X-Tinfoil-Usage-Metrics"

	// CORS headers allowed in requests
	allowHeaders = "Accept, Authorization, Content-Type, Ehbp-Encapsulated-Key, X-Tinfoil-Enclave-Url, X-Language, X-User-Tier"

	// CORS headers exposed to the browser in responses
	exposeHeaders = "Ehbp-Response-Nonce"
)

// These encryption headers must be preserved for the protocol to work
var (
	ehbpRequestHeaders  = []string{"Ehbp-Encapsulated-Key"}
	ehbpResponseHeaders = []string{"Ehbp-Response-Nonce"}
)

func main() {
	http.HandleFunc("/v1/chat/completions", proxyHandler)
	http.HandleFunc("/attestation", attestationHandler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("proxy listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func proxyHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("Received %s request from %s", r.Method, r.RemoteAddr)

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", allowHeaders)
	w.Header().Set("Access-Control-Expose-Headers", exposeHeaders)

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Get upstream URL from the X-Tinfoil-Enclave-Url header (points to the function enclave)
	upstreamBase := r.Header.Get(enclaveURLHeader)
	if upstreamBase == "" {
		log.Println("Error: X-Tinfoil-Enclave-Url header not provided")
		http.Error(w, "X-Tinfoil-Enclave-Url header required", http.StatusBadRequest)
		return
	}
	upstreamURL := upstreamBase + r.URL.Path

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	if accept := r.Header.Get("Accept"); accept != "" {
		req.Header.Set("Accept", accept)
	}

	// Optional: forward API key if configured (for function-level auth)
	if apiKey := os.Getenv("TINFOIL_API_KEY"); apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	// Request usage metrics from Tinfoil for billing
	req.Header.Set(usageMetricsRequestHeader, "true")

	// Required: Copy encryption headers from the client request
	copyHeaders(req.Header, r.Header, ehbpRequestHeaders...)

	// Business logic: enrich the upstream request with function-specific headers
	setLanguageHeader(req.Header, r.Header)
	setAllowedModelsHeader(req.Header, r.Header)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Required: Copy encryption headers from the upstream response
	copyHeaders(w.Header(), resp.Header, ehbpResponseHeaders...)

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}

	// Log usage metrics from response header (non-streaming) for billing
	if usage := resp.Header.Get(usageMetricsResponseHeader); usage != "" {
		log.Printf("Usage metrics (header): %s", usage)
	}

	if te := resp.Header.Get("Transfer-Encoding"); te != "" {
		w.Header().Set("Transfer-Encoding", te)
		w.Header().Del("Content-Length")
	}

	w.WriteHeader(resp.StatusCode)

	if flusher, ok := w.(http.Flusher); ok {
		fw := &flushWriter{w: w, f: flusher}
		if _, copyErr := io.Copy(fw, resp.Body); copyErr != nil {
			log.Printf("stream copy failed: %v", copyErr)
		}
	} else {
		if _, copyErr := io.Copy(w, resp.Body); copyErr != nil {
			log.Printf("response copy failed: %v", copyErr)
		}
	}

	// After body is fully read, log usage metrics from trailer (streaming) for billing
	if usage := resp.Trailer.Get(usageMetricsResponseHeader); usage != "" {
		log.Printf("Usage metrics (trailer): %s", usage)
	}
}

type flushWriter struct {
	w io.Writer
	f http.Flusher
}

func (fw *flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	fw.f.Flush()
	return n, err
}

// setLanguageHeader forwards the client's language preference to the function.
// The function uses this to template the system prompt.
func setLanguageHeader(dst, src http.Header) {
	if language := src.Get("X-Language"); language != "" {
		dst.Set("X-Language", language)
	}
}

// setAllowedModelsHeader determines which models the user can access based on
// their tier and tells the function via a header. The function enforces this
// against the model in the (encrypted) request body.
func setAllowedModelsHeader(dst, src http.Header) {
	userTier := src.Get("X-User-Tier")
	if userTier == "paid" {
		dst.Set("X-Allowed-Models", "gpt-oss-120b,kimi-k2-5")
	} else {
		dst.Set("X-Allowed-Models", "gpt-oss-120b")
	}
	log.Printf("User tier: %s, allowed models: %s", userTier, dst.Get("X-Allowed-Models"))
}

func copyHeaders(dst, src http.Header, keys ...string) {
	for _, key := range keys {
		if value := src.Get(key); value != "" {
			dst.Set(key, value)
		}
	}
}

func attestationHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("Received attestation %s request from %s", r.Method, r.RemoteAddr)

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	req, err := http.NewRequest(r.Method, "https://atc.tinfoil.sh/attestation", r.Body)
	if err != nil {
		log.Printf("Failed to create attestation request: %v", err)
		http.Error(w, "Failed to create attestation request", http.StatusInternalServerError)
		return
	}
	if r.Method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("Failed to fetch attestation bundle: %v", err)
		http.Error(w, "Failed to fetch attestation bundle", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}

	w.WriteHeader(resp.StatusCode)
	if _, copyErr := io.Copy(w, resp.Body); copyErr != nil {
		log.Printf("attestation response copy failed: %v", copyErr)
	}
}
