package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
)

const (
	defaultSystemPrompt = "You are a helpful assistant. Always respond in {{LANGUAGE}}."
	maxLanguageLen      = 64
)

var languageRegex = regexp.MustCompile(`^[a-zA-Z][a-zA-Z \-]*$`)

func main() {
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	http.HandleFunc("/v1/chat/completions", chatHandler)

	log.Println("Function server listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func chatHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 1. Read and validate language header
	language := r.Header.Get("X-Language")
	if language == "" {
		language = "English"
	}
	if len(language) > maxLanguageLen || !languageRegex.MatchString(language) {
		http.Error(w, "Invalid X-Language header", http.StatusBadRequest)
		return
	}

	// 2. Read request body (already decrypted by tfshim)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("Failed to read request body: %v", err)
		http.Error(w, "Failed to read request body", http.StatusInternalServerError)
		return
	}
	defer r.Body.Close()

	// 3. Parse as generic map to preserve all OpenAI fields
	var reqBody map[string]interface{}
	if err := json.Unmarshal(body, &reqBody); err != nil {
		log.Printf("Failed to parse request body: %v", err)
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}

	// 4. Render system prompt
	promptTemplate := os.Getenv("SYSTEM_PROMPT_TEMPLATE")
	if promptTemplate == "" {
		promptTemplate = defaultSystemPrompt
	}
	systemPrompt := strings.ReplaceAll(promptTemplate, "{{LANGUAGE}}", language)

	// 5. Prepend system message
	messages, _ := reqBody["messages"].([]interface{})
	systemMessage := map[string]interface{}{
		"role":    "system",
		"content": systemPrompt,
	}
	reqBody["messages"] = append([]interface{}{systemMessage}, messages...)

	// 6. Override model if configured
	if model := os.Getenv("TINFOIL_MODEL"); model != "" {
		reqBody["model"] = model
	}

	// 7. Marshal modified body
	modifiedBody, err := json.Marshal(reqBody)
	if err != nil {
		log.Printf("Failed to marshal modified body: %v", err)
		http.Error(w, "Failed to marshal request", http.StatusInternalServerError)
		return
	}

	// 8. Forward to inference (plain HTTPS — already inside the enclave)
	inferenceURL := os.Getenv("TINFOIL_INFERENCE_URL")
	if inferenceURL == "" {
		http.Error(w, "TINFOIL_INFERENCE_URL not configured", http.StatusInternalServerError)
		return
	}
	apiKey := os.Getenv("TINFOIL_API_KEY")
	if apiKey == "" {
		http.Error(w, "TINFOIL_API_KEY not configured", http.StatusInternalServerError)
		return
	}

	upstreamURL := strings.TrimRight(inferenceURL, "/") + "/v1/chat/completions"
	upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(modifiedBody))
	if err != nil {
		log.Printf("Failed to create upstream request: %v", err)
		http.Error(w, "Failed to create upstream request", http.StatusInternalServerError)
		return
	}

	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Authorization", "Bearer "+apiKey)
	if accept := r.Header.Get("Accept"); accept != "" {
		upstreamReq.Header.Set("Accept", accept)
	}

	resp, err := http.DefaultClient.Do(upstreamReq)
	if err != nil {
		log.Printf("Upstream request failed: %v", err)
		http.Error(w, "Upstream request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// 9. Stream response back — tfshim re-encrypts via EHBP transparently
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if te := resp.Header.Get("Transfer-Encoding"); te != "" {
		w.Header().Set("Transfer-Encoding", te)
		w.Header().Del("Content-Length")
	}

	w.WriteHeader(resp.StatusCode)

	if flusher, ok := w.(http.Flusher); ok {
		fw := &flushWriter{w: w, f: flusher}
		if _, err := io.Copy(fw, resp.Body); err != nil {
			log.Printf("Stream copy failed: %v", err)
		}
	} else {
		if _, err := io.Copy(w, resp.Body); err != nil {
			log.Printf("Response copy failed: %v", err)
		}
	}

	log.Printf("Chat completion forwarded (language=%s, status=%d)", language, resp.StatusCode)
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
