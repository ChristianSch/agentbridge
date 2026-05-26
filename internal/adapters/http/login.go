package httpadapter

import (
	"net/http"
	"strings"
)

const loginHTML = `<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Unlock AgentBridge</title>
<style>body{margin:0;min-height:100dvh;display:grid;place-items:center;background:#141414;color:#d7c7a1;font:15px ui-monospace,SFMono-Regular,Menlo,monospace}.box{width:min(520px,calc(100vw - 32px));border:1px solid #4b4638;background:#151515;padding:18px;box-shadow:0 20px 80px #050505aa}h1{margin:0 0 8px;font-size:20px}p{color:#9f926f}.row{display:flex;gap:8px;flex-wrap:wrap}button{min-height:40px;border:1px solid #6f684f;background:#25251f;color:#d7c7a1;font:inherit;font-weight:800;padding:0 13px}button.primary{background:#12324a;border-color:#00afff;color:#f0e4bd}input{min-height:40px;border:1px solid #4b4638;background:#0c0c0c;color:#d7c7a1;padding:0 10px;font:inherit;flex:1}.err{color:#ffb0a0;white-space:pre-wrap}</style></head>
<body><main class="box"><h1>Unlock AgentBridge</h1><p id="status">Checking authentication…</p><div class="row"><button id="enter" class="primary" style="display:none">Enter AgentBridge</button><button id="login" class="primary">Use Face ID / security key</button><button id="register">Register passkey</button></div><p>Bootstrap token</p><div class="row"><input id="token" placeholder="AGENTBRIDGE_TOKEN"><button id="tokenBtn">Use token</button></div><p class="err" id="err"></p></main>
<script nonce="{{CSP_NONCE}}">
localStorage.removeItem('agentbridgeToken');
const $=id=>document.getElementById(id);
function b64uToBuf(s){s=s.replace(/-/g,'+').replace(/_/g,'/'); while(s.length%4)s+='='; return Uint8Array.from(atob(s),c=>c.charCodeAt(0)).buffer}
function bufToB64u(b){return btoa(String.fromCharCode(...new Uint8Array(b))).replace(/\+/g,'-').replace(/\//g,'_').replace(/=+$/,'')}
function prepCreate(o){o.publicKey.challenge=b64uToBuf(o.publicKey.challenge); o.publicKey.user.id=b64uToBuf(o.publicKey.user.id); if(o.publicKey.excludeCredentials) for(const c of o.publicKey.excludeCredentials)c.id=b64uToBuf(c.id); return o}
function prepGet(o){o.publicKey.challenge=b64uToBuf(o.publicKey.challenge); if(o.publicKey.allowCredentials) for(const c of o.publicKey.allowCredentials)c.id=b64uToBuf(c.id); return o}
function credJSON(c){let r={id:c.id,rawId:bufToB64u(c.rawId),type:c.type,response:{clientDataJSON:bufToB64u(c.response.clientDataJSON)}}; if(c.response.attestationObject)r.response.attestationObject=bufToB64u(c.response.attestationObject); if(c.response.authenticatorData)r.response.authenticatorData=bufToB64u(c.response.authenticatorData); if(c.response.signature)r.response.signature=bufToB64u(c.response.signature); if(c.response.userHandle)r.response.userHandle=bufToB64u(c.response.userHandle); return r}
async function api(p,opt={}){const token=$('token').value.trim(); opt.headers={...(opt.headers||{}),...(token?{Authorization:'Bearer '+token}:{})}; const res=await fetch(p,opt); if(!res.ok)throw new Error(await res.text()); return res.json()}
let authStatus={passkeys:false,registered:false,authenticated:false};
async function status(){try{const s=await api('/auth/status'); authStatus=s; $('login').disabled=!s.passkeys||!s.registered; $('register').disabled=!s.passkeys; if(s.authenticated){$('status').textContent='Unlocked.'; $('enter').style.display='inline-block'} else if(!s.passkeys){$('status').textContent='Passkeys are not enabled on this server. Enter the bootstrap token to unlock.'} else {$('status').textContent=s.registered?'Passkey required.':'No passkey registered yet. Enter the bootstrap token, then register a passkey.'}}catch(e){$('status').textContent='Locked.'}}
async function login(){try{$('err').textContent=''; if(!authStatus.passkeys)throw new Error('Passkeys are not enabled on this server. Add auth.passkeys to agentbridge.yaml and restart.'); if(!authStatus.registered)throw new Error('No passkey is registered yet. Enter the bootstrap token, then register one.'); const o=prepGet(await api('/auth/passkey/login/begin',{method:'POST'})); const c=await navigator.credentials.get(o); await api('/auth/passkey/login/finish',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(credJSON(c))}); location.href='/'}catch(e){$('err').textContent=e.message}}
async function register(){try{$('err').textContent=''; if(!authStatus.passkeys)throw new Error('Passkeys are not enabled on this server. Add auth.passkeys to agentbridge.yaml and restart.'); const o=prepCreate(await api('/auth/passkey/register/begin',{method:'POST'})); const c=await navigator.credentials.create(o); await api('/auth/passkey/register/finish',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(credJSON(c))}); location.href='/'}catch(e){$('err').textContent=e.message}}
async function useToken(){try{$('err').textContent=''; const token=$('token').value.trim(); if(!token)throw new Error('Enter the AgentBridge token.'); const s=await fetch('/auth/token',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({token})}).then(async r=>{if(!r.ok)throw new Error(await r.text()); return r.json()}); if(s.passkeys){$('status').textContent=s.registered?'Token accepted. Now unlock with your passkey.':'Token accepted. Register a passkey to own this browser.'; if(s.registered) await login(); else await register(); return} location.href='/'}catch(e){$('err').textContent=e.message}}
$('enter').onclick=()=>{location.href='/'}; $('login').onclick=login; $('register').onclick=register; $('tokenBtn').onclick=useToken; status();
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
