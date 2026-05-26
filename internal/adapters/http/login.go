package httpadapter

import (
	"net/http"
	"strings"
)

const loginHTML = `<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover"><title>Unlock AgentBridge</title>
<style>
:root{color-scheme:dark;--bg:#141414;--panel:#151515;--line:#4b4638;--line-strong:#6f684f;--text:#d7c7a1;--muted:#9f926f;--sky:#00afff;--button:#25251f;--blue:#12324a;--err:#ffb0a0;--shadow:#050505aa}
*{box-sizing:border-box}body{margin:0;min-height:100dvh;display:grid;place-items:center;padding:16px;background:radial-gradient(70vw 50vh at 50% 35%,#1c1a15 0,#141414 58%);color:var(--text);font:15px/1.45 ui-monospace,SFMono-Regular,Menlo,monospace}.box{width:min(460px,100%);border:1px solid var(--line);background:var(--panel);padding:18px;box-shadow:0 20px 80px var(--shadow)}h1{margin:0 0 8px;font-size:20px;line-height:1.15}.status{margin:0 0 16px;color:var(--muted)}.panel{display:none}.panel.active{display:block}.token-form{display:grid;grid-template-columns:1fr auto;gap:8px}.passkey-actions{display:grid;gap:8px}.hint{margin:12px 0 0;color:var(--muted);font-size:13px}.err{min-height:1.4em;margin:12px 0 0;color:var(--err);white-space:pre-wrap}button{min-height:40px;border:1px solid var(--line-strong);background:var(--button);color:var(--text);font:inherit;font-weight:800;padding:0 13px}button.primary{background:var(--blue);border-color:var(--sky);color:#f0e4bd}button:disabled{display:none}input{min-width:0;min-height:40px;border:1px solid var(--line);background:#0c0c0c;color:var(--text);padding:0 10px;font:inherit}input:focus{outline:0;border-color:var(--sky);box-shadow:0 0 0 2px #00afff33}.secondary{width:100%;margin-top:10px;background:transparent;color:var(--muted);box-shadow:none}@media(max-width:480px){body{display:block;padding:0;background:#141414}.box{width:100%;min-height:100dvh;border:0;padding:18px 14px;box-shadow:none}.token-form{grid-template-columns:1fr}.token-form button,.passkey-actions button{width:100%}h1{font-size:18px}.status{margin-bottom:14px}input{font-size:16px}}
</style></head>
<body><main class="box"><h1>Unlock AgentBridge</h1><p class="status" id="status">Checking authentication…</p>
<section id="tokenPanel" class="panel"><form class="token-form" id="tokenForm"><input id="token" autocomplete="current-password" placeholder="AgentBridge token"><button id="tokenBtn" class="primary">Unlock</button></form><p class="hint">Token auth is enabled. Enter the server token to continue.</p></section>
<section id="passkeyPanel" class="panel"><div class="passkey-actions"><button id="login" class="primary">Use Face ID / security key</button><button id="register">Register passkey</button></div><button id="showToken" class="secondary" type="button">Use bootstrap token instead</button></section>
<section id="enterPanel" class="panel"><button id="enter" class="primary">Enter AgentBridge</button></section>
<p class="err" id="err"></p></main>
<script nonce="{{CSP_NONCE}}">
localStorage.removeItem('agentbridgeToken');
const $=id=>document.getElementById(id);
function show(id){for(const el of document.querySelectorAll('.panel'))el.classList.toggle('active',el.id===id)}
function b64uToBuf(s){s=s.replace(/-/g,'+').replace(/_/g,'/'); while(s.length%4)s+='='; return Uint8Array.from(atob(s),c=>c.charCodeAt(0)).buffer}
function bufToB64u(b){return btoa(String.fromCharCode(...new Uint8Array(b))).replace(/\+/g,'-').replace(/\//g,'_').replace(/=+$/,'')}
function prepCreate(o){o.publicKey.challenge=b64uToBuf(o.publicKey.challenge); o.publicKey.user.id=b64uToBuf(o.publicKey.user.id); if(o.publicKey.excludeCredentials) for(const c of o.publicKey.excludeCredentials)c.id=b64uToBuf(c.id); return o}
function prepGet(o){o.publicKey.challenge=b64uToBuf(o.publicKey.challenge); if(o.publicKey.allowCredentials) for(const c of o.publicKey.allowCredentials)c.id=b64uToBuf(c.id); return o}
function credJSON(c){let r={id:c.id,rawId:bufToB64u(c.rawId),type:c.type,response:{clientDataJSON:bufToB64u(c.response.clientDataJSON)}}; if(c.response.attestationObject)r.response.attestationObject=bufToB64u(c.response.attestationObject); if(c.response.authenticatorData)r.response.authenticatorData=bufToB64u(c.response.authenticatorData); if(c.response.signature)r.response.signature=bufToB64u(c.response.signature); if(c.response.userHandle)r.response.userHandle=bufToB64u(c.response.userHandle); return r}
async function api(p,opt={}){const token=$('token').value.trim(); opt.headers={...(opt.headers||{}),...(token?{Authorization:'Bearer '+token}:{})}; const res=await fetch(p,opt); if(!res.ok)throw new Error(await res.text()); return res.json()}
let authStatus={passkeys:false,registered:false,authenticated:false};
async function status(){try{const s=await api('/auth/status'); authStatus=s; $('err').textContent=''; if(s.authenticated){$('status').textContent='Unlocked.'; show('enterPanel'); return} if(!s.passkeys){$('status').textContent='Enter your AgentBridge token.'; show('tokenPanel'); $('token').focus(); return} if(s.registered){$('status').textContent='Unlock with your passkey.'; $('login').style.display='block'; $('register').style.display='none'; show('passkeyPanel'); return} $('status').textContent='First setup: enter the bootstrap token, then register a passkey.'; show('tokenPanel'); $('token').focus()}catch(e){$('status').textContent='Locked.'; show('tokenPanel')}}
async function login(){try{$('err').textContent=''; if(!authStatus.passkeys)throw new Error('Passkeys are not enabled on this server.'); if(!authStatus.registered)throw new Error('No passkey is registered yet. Enter the bootstrap token first.'); const o=prepGet(await api('/auth/passkey/login/begin',{method:'POST'})); const c=await navigator.credentials.get(o); await api('/auth/passkey/login/finish',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(credJSON(c))}); location.href='/'}catch(e){$('err').textContent=e.message}}
async function register(){try{$('err').textContent=''; if(!authStatus.passkeys)throw new Error('Passkeys are not enabled on this server.'); const o=prepCreate(await api('/auth/passkey/register/begin',{method:'POST'})); const c=await navigator.credentials.create(o); await api('/auth/passkey/register/finish',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(credJSON(c))}); location.href='/'}catch(e){$('err').textContent=e.message}}
async function useToken(e){e&&e.preventDefault(); try{$('err').textContent=''; const token=$('token').value.trim(); if(!token)throw new Error('Enter the AgentBridge token.'); const s=await fetch('/auth/token',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({token})}).then(async r=>{if(!r.ok)throw new Error(await r.text()); return r.json()}); authStatus=s; if(s.passkeys){$('status').textContent=s.registered?'Token accepted. Unlock with your passkey.':'Token accepted. Register a passkey for this browser.'; $('login').style.display=s.registered?'block':'none'; $('register').style.display=s.registered?'none':'block'; show('passkeyPanel'); if(s.registered) await login(); else await register(); return} location.href='/'}catch(e){$('err').textContent=e.message}}
$('enter').onclick=()=>{location.href='/'}; $('login').onclick=login; $('register').onclick=register; $('tokenForm').onsubmit=useToken; $('showToken').onclick=()=>{show('tokenPanel'); $('status').textContent='Enter your AgentBridge token.'; $('token').focus()}; status();
</script></body></html>`

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	nonce := randString(18)
	setSecurityHeaders(w, nonce)
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(strings.ReplaceAll(loginHTML, "{{CSP_NONCE}}", nonce)))
}
