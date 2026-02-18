import { SecureClient, TinfoilError, AttestationError } from "tinfoil";

type Role = "user" | "assistant";

function requireElement<T extends Element>(selector: string): T {
  const element = document.querySelector<T>(selector);
  if (!element) {
    throw new Error(`Missing element for selector: ${selector}`);
  }
  return element;
}

const messages = requireElement<HTMLDivElement>("#messages");
const input = requireElement<HTMLInputElement>("#messageInput");
const sendButton = requireElement<HTMLButtonElement>("#sendBtn");
const languageSelect = requireElement<HTMLSelectElement>("#languageSelect");
const modelSelect = requireElement<HTMLSelectElement>("#modelSelect");
const paidToggle = requireElement<HTMLInputElement>("#paidToggle");

// Configure the client to attest the FUNCTION enclave (not inference directly).
// The SDK verifies the function's code via configRepo, fetches its HPKE key,
// and encrypts request bodies end-to-end to the function enclave.
// The proxy sees only ciphertext and forwards it along with plaintext headers.
const client = new SecureClient({
  baseURL: "http://localhost:8080/",
  attestationBundleURL: "http://localhost:8080",
  configRepo: "tinfoilsh/system-prompt-functions-example", // verify the function's code, not the model-router
  enclaveURL: "https://system-prompt-injector.tinfoil.functions.tinfoil.sh", // function enclave
});

function appendMessage(text: string, role: Role): HTMLDivElement {
  const wrapper = document.createElement("div");
  wrapper.className = `message ${role}`;

  const bubble = document.createElement("div");
  bubble.className = "message-content";
  bubble.textContent = text;

  wrapper.appendChild(bubble);
  messages.appendChild(wrapper);
  messages.scrollTop = messages.scrollHeight;

  return bubble;
}

function processEvent(
  payload: string,
  onChunk: (text: string) => void,
): boolean {
  if (payload === "[DONE]") {
    return true;
  }

  try {
    const message = JSON.parse(payload);
    const text =
      message.choices?.[0]?.delta?.content ??
      message.choices?.[0]?.message?.content ??
      "";

    if (text) {
      onChunk(text);
    }

    if (message.error?.message) {
      onChunk(`\nError: ${message.error.message}`);
      return true;
    }
  } catch (error) {
    console.warn("Could not parse SSE chunk", payload, error);
  }

  return false;
}

async function streamResponse(
  response: Response,
  onChunk: (text: string) => void,
) {
  const body = response.body;
  if (!body) return;

  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  let finished = false;

  const flush = (final = false) => {
    const segments = buffer.split("\n\n");
    buffer = final ? "" : (segments.pop() ?? "");

    for (const segment of segments) {
      const dataLines = segment
        .split("\n")
        .filter((line) => line.startsWith("data:"))
        .map((line) => line.slice(5).trim());

      if (dataLines.length === 0) {
        continue;
      }

      const payload = dataLines.join("\n");
      finished ||= processEvent(payload, onChunk);
      if (finished) {
        buffer = "";
        return;
      }
    }
  };

  while (!finished) {
    const { value, done } = await reader.read();
    if (value) {
      buffer += decoder.decode(value, { stream: true });
      flush();
    }
    if (done) {
      break;
    }
  }

  buffer += decoder.decode();
  flush(true);
}

async function sendMessage(): Promise<void> {
  const text = input.value.trim();
  if (!text) {
    input.focus();
    return;
  }

  const language = languageSelect.value;
  const model = modelSelect.value;
  const userTier = paidToggle.checked ? "paid" : "free";

  input.value = "";
  appendMessage(text, "user");
  sendButton.disabled = true;

  try {
    // Wait for function enclave attestation to complete.
    // TinfoilError: transient network issue -- just call ready() again to retry.
    // AttestationError: security issue -- call reset() to get a fresh bundle and re-verify.
    try {
      await client.ready();
    } catch (err) {
      if (err instanceof TinfoilError) {
        await client.ready();
      } else if (err instanceof AttestationError) {
        client.reset();
        await client.ready();
      } else {
        throw err;
      }
    }

    const response = await client.fetch("/v1/chat/completions", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Accept: "text/event-stream",
        "X-Language": language,    // Controls the system prompt language
        "X-User-Tier": userTier,   // Proxy uses this to set allowed models
      },
      body: JSON.stringify({
        model,
        messages: [{ role: "user", content: text }],
        stream: true,
      }),
    });

    if (!response.ok) {
      const errorText = await response.text();
      throw new Error(errorText || `HTTP ${response.status}`);
    }

    const assistantBubble = appendMessage("", "assistant");
    const contentType = response.headers.get("Content-Type") ?? "";

    if (contentType.includes("text/event-stream")) {
      await streamResponse(response, (chunk) => {
        assistantBubble.textContent += chunk;
        messages.scrollTop = messages.scrollHeight;
      });
      return;
    }

    const json = await response.json();
    assistantBubble.textContent =
      json.choices?.[0]?.message?.content ?? "No content";
  } catch (error) {
    console.error("Chat request failed", error);
    const message =
      error instanceof Error ? error.message : "Could not connect to server";
    appendMessage(`Error: ${message}`, "assistant");
  } finally {
    sendButton.disabled = false;
    input.focus();
  }
}

sendButton.addEventListener("click", () => void sendMessage());
input.addEventListener("keydown", (event) => {
  if (event.key === "Enter" && !event.isComposing) {
    event.preventDefault();
    void sendMessage();
  }
});

input.focus();
