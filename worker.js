// xtune's Cloudflare Worker: VLESS over WebSocket in, plain TCP out to the xtune server.
// ROUTES maps a secret path to the origin "IP:port" of a worker-ws inbound. Each connection
// starts with a PROXY protocol v1 line carrying the client's address, so the server's device
// limits and online lists see the client rather than Cloudflare. Anything else gets a 404.
import { connect } from "cloudflare:sockets";

export default {
  async fetch(request, env) {
    const routes = JSON.parse(env.ROUTES || "{}");
    const origin = routes[new URL(request.url).pathname];
    if (!origin || request.headers.get("Upgrade") !== "websocket") {
      return new Response("Not Found", { status: 404 });
    }
    const pair = new WebSocketPair();
    const ws = pair[1];
    ws.accept();
    // Xray sends the first bytes in this header ("ed" early data), base64url without padding
    const early = request.headers.get("Sec-WebSocket-Protocol");
    relay(ws, origin, request.headers.get("CF-Connecting-IP") || "", early);
    const headers = early ? { "Sec-WebSocket-Protocol": early } : {};
    return new Response(null, { status: 101, webSocket: pair[0], headers });
  },
};

function relay(ws, origin, client, early) {
  const i = origin.lastIndexOf(":");
  const host = origin.slice(0, i).replace(/^\[|\]$/g, "");
  const port = Number(origin.slice(i + 1));
  let sock;
  const close = () => {
    try { ws.close(1000); } catch (e) {}
    try { sock && sock.close(); } catch (e) {}
  };
  try {
    sock = connect({ hostname: host, port });
  } catch (e) {
    close();
    return;
  }
  const writer = sock.writable.getWriter();
  // listeners go on before anything is awaited, so no early message is lost
  let chain = writer.write(proxyHeader(client, host, port));
  if (early) {
    chain = chain.then(() => writer.write(unb64(early)));
  }
  ws.addEventListener("message", (ev) => {
    const data = typeof ev.data === "string" ? new TextEncoder().encode(ev.data) : new Uint8Array(ev.data);
    chain = chain.then(() => writer.write(data)).catch(close);
  });
  ws.addEventListener("close", close);
  ws.addEventListener("error", close);
  sock.readable
    .pipeTo(new WritableStream({ write(chunk) { ws.send(chunk); } }))
    .catch(() => {})
    .finally(close);
}

function proxyHeader(client, host, port) {
  if (!client) {
    return new TextEncoder().encode("PROXY UNKNOWN\r\n");
  }
  const v6 = client.includes(":");
  let dst = host;
  if (v6 && !host.includes(":")) dst = "::ffff:" + host;
  if (!v6 && host.includes(":")) dst = "127.0.0.1";
  const sport = 1024 + Math.floor(Math.random() * 64000);
  return new TextEncoder().encode(`PROXY ${v6 ? "TCP6" : "TCP4"} ${client} ${dst} ${sport} ${port}\r\n`);
}

function unb64(s) {
  s = s.replace(/-/g, "+").replace(/_/g, "/");
  while (s.length % 4) s += "=";
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}
