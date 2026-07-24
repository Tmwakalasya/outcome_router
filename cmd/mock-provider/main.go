package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

type requestBody struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Messages []struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	} `json:"messages"`
}

func main() {
	address := os.Getenv("MOCK_PROVIDER_ADDRESS")
	if address == "" {
		address = ":8091"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", complete)
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]string{"status": "ok"})
	})
	log.Printf("mock provider listening on %s", address)
	log.Fatal(http.ListenAndServe(address, mux))
}

func complete(writer http.ResponseWriter, request *http.Request) {
	var body requestBody
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		http.Error(writer, `{"error":{"message":"invalid request"}}`, http.StatusBadRequest)
		return
	}
	if strings.Contains(body.Model, "unavailable") {
		http.Error(writer, `{"error":{"message":"simulated outage"}}`, http.StatusServiceUnavailable)
		return
	}
	content := fmt.Sprintf("Mock response from %s", body.Model)
	if body.Stream {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set("Cache-Control", "no-cache")
		flusher, _ := writer.(http.Flusher)
		buffer := bufio.NewWriter(writer)
		for index, word := range strings.Fields(content) {
			chunk := map[string]any{
				"id":      "chatcmpl_mock",
				"object":  "chat.completion.chunk",
				"created": time.Now().Unix(),
				"model":   body.Model,
				"choices": []map[string]any{{
					"index": index,
					"delta": map[string]string{"content": word + " "},
				}},
			}
			encoded, _ := json.Marshal(chunk)
			_, _ = fmt.Fprintf(buffer, "data: %s\n\n", encoded)
			_ = buffer.Flush()
			if flusher != nil {
				flusher.Flush()
			}
		}
		_, _ = fmt.Fprint(buffer, "data: [DONE]\n\n")
		_ = buffer.Flush()
		if flusher != nil {
			flusher.Flush()
		}
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"id":      "chatcmpl_mock",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   body.Model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]string{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
		"usage": map[string]int{
			"prompt_tokens": 42, "completion_tokens": 18, "total_tokens": 60,
		},
	})
}
