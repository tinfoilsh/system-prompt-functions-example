# System Prompt Injector — Tinfoil Function Example

A Tinfoil Function that intercepts chat completion requests, injects a secret system prompt (templated with a `{{LANGUAGE}}` placeholder), and forwards the modified request to inference. The request body is encrypted end-to-end from the client to the function enclave via EHBP.

```
Client ──EHBP encrypted──▶ Proxy ──EHBP encrypted──▶ tfshim ──plaintext──▶ Function ──HTTPS──▶ Inference
         (browser)          (can't read body)         (decrypts)            (injects prompt)
```

## Local Testing

### 1. Run the function server

```bash
cd function

export TINFOIL_API_KEY="sk-..."                # your Tinfoil inference API key
export TINFOIL_INFERENCE_URL="https://inference.tinfoil.sh"
export TINFOIL_MODEL="gpt-oss-120b"
export SYSTEM_PROMPT_TEMPLATE="You are a helpful assistant. Always respond in {{LANGUAGE}}."

go run .
```

The function listens on `:8080`. Locally (without tfshim), it receives plaintext HTTP — you can test it directly with curl.

### 2. Test with curl

```bash
# Default language (English)
curl -s http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-oss-120b",
    "messages": [{"role": "user", "content": "What is the capital of Germany?"}]
  }' | jq .

# French — the injected system prompt tells the model to respond in French
curl -s http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "X-Language: French" \
  -d '{
    "model": "gpt-oss-120b",
    "messages": [{"role": "user", "content": "What is the capital of Germany?"}]
  }' | jq .

# Streaming
curl -N http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Accept: text/event-stream" \
  -H "X-Language: Spanish" \
  -d '{
    "model": "gpt-oss-120b",
    "messages": [{"role": "user", "content": "What is the capital of Germany?"}],
    "stream": true
  }'

# Health check
curl http://localhost:8080/healthz
```

### 3. Run the proxy (optional, for browser client testing)

In a separate terminal:

```bash
cd proxy
go run .
```

The proxy listens on `:8080`, so stop the function server first, or change its port. For local testing where both need to run simultaneously, start the function on a different port:

```bash
# Terminal 1: function on port 8081
cd function
go run . &  # or change the listen address in main.go temporarily
# Terminal 2: proxy on port 8080
cd proxy
go run .
```

Note: the proxy expects the function enclave URL via the `X-Tinfoil-Enclave-Url` header from the client SDK. Locally without a real enclave, use curl to test the proxy:

```bash
curl -s http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "X-Tinfoil-Enclave-Url: http://localhost:8081" \
  -H "X-Language: Japanese" \
  -d '{
    "model": "gpt-oss-120b",
    "messages": [{"role": "user", "content": "What is the capital of Germany?"}],
    "stream": false
  }'
```

### 4. Run the browser client

```bash
cd client
npm install
npx vite
```

Opens at `http://localhost:5173`. The browser client uses `SecureClient` with EHBP encryption, so it requires a deployed function enclave to perform attestation. For local-only testing, use curl as shown above.

## Deploying to Tinfoil

### Build and push the container

```bash
cd function
docker build -t ghcr.io/tinfoilsh/system-prompt-injector:0.0.1 .
docker push ghcr.io/tinfoilsh/system-prompt-injector:0.0.1
```

### Set secrets

Configure `TINFOIL_API_KEY` and `SYSTEM_PROMPT_TEMPLATE` as Tinfoil secrets. These are only accessible inside the attested enclave at runtime.

### Deploy

Tag a release to trigger the GitHub Actions workflow (`.github/workflows/build.yml`), which builds the container and creates an attestation via `tinfoilsh/pri-build-action`.

### Update the client

Once deployed, update `enclaveURL` in `client/main.ts` to match the function's enclave address, and `configRepo` to match the GitHub repo.

## Project Structure

```
function/
  main.go          # HTTP server: parses body, injects system prompt, forwards to inference
  go.mod
  Dockerfile
proxy/
  main.go          # Forwards encrypted bodies + X-Language header to the function enclave
  go.mod
client/
  main.ts          # Browser client with EHBP encryption and language selector
  index.html
  styles.css
  package.json
tinfoil-config.yml # Enclave config (tfshim + function container + secrets)
```
