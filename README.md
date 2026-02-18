# System Prompt Injector

A Tinfoil Function that prepends a secret system prompt to chat completion requests before forwarding them to inference. The system prompt is stored as an enclave secret and templated with a `{{LANGUAGE}}` placeholder controlled by the client via an `X-Language` header.

```
Client ──EHBP encrypted──▶ Proxy ──encrypted──▶ Function Enclave ──HTTPS──▶ Inference
                           (can't read body)     (decrypts, injects
                                                  system prompt)
```

The request body is encrypted end-to-end from client to the function enclave. The proxy forwards ciphertext. The system prompt and inference API key never leave the enclave.

## How it works

The function server (`function/main.go`) receives a decrypted chat completion request from tfshim, prepends a system message rendered from `SYSTEM_PROMPT_TEMPLATE` with the `X-Language` header value, and proxies the modified request to Tinfoil inference.

The proxy also enforces model access: it reads the client's `X-User-Tier` header (paid/free) and sets an `X-Allowed-Models` header that the function checks against the model in the request body. Free users can only use `gpt-oss-120b`; paid users can also use `kimi-k2-5`. The proxy can't read the model from the encrypted body — it just sets the policy. The function, which can read the body, enforces it.

## Quick start

### 1. Run the proxy

```bash
cd proxy
go run .
```

Listens on `:8080`. Forwards encrypted requests and the `X-Language` header to the function enclave.

### 2. Run the browser client

```bash
cd client
npm install
npx vite
```

Opens at `http://localhost:5173`. Pick a language from the dropdown and send a message. The client attests the function enclave, encrypts the request body end-to-end via EHBP, and streams the response.

Update `enclaveURL` and `configRepo` in `client/main.ts` to point to your deployed function.

## Deploying the function

The root `Dockerfile` builds only the function server. The proxy and client run outside the enclave.

1. Configure `TINFOIL_API_KEY` and `SYSTEM_PROMPT_TEMPLATE` as Tinfoil secrets
2. Push a version tag to trigger `.github/workflows/build.yml`, which builds and pushes the Docker image to GHCR, then creates a Sigstore attestation via `tinfoilsh/pri-build-action`

## Structure

| Path | Runs in | Description |
|------|---------|-------------|
| `function/` | Enclave | Go server — injects system prompt, forwards to inference |
| `proxy/` | Your backend | Forwards encrypted requests, sets `X-Allowed-Models` based on user tier |
| `client/` | Browser | Chat UI with language, model, and tier selectors |
| `Dockerfile` | CI | Builds the function server only |
| `tinfoil-config.yml` | CI | Enclave config (tfshim + secrets) |
