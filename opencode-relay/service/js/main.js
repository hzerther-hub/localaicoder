const RELAY=true;
const D=new URLSearchParams(location.search).get('d');
const WS=(location.protocol==='https:'?'wss://':'ws://')+location.host+'/s/ws?d='+D;
const $=id=>document.getElementById(id);
const log=$('log');
let ALL=[],wsCur='',sid='',runs=new Set(),cur=null,ws,rid=0,lastCurrent='',openSeq=0,permId=null,permSid='',projPin=false;
const pend={};
const esc=s=>{const d=document.createElement('div');d.textContent=s;return d.innerHTML;};
const ago=ts=>{const d=Date.now()/1000-ts;if(d<60)return '刚刚';if(d<3600)return Math.floor(d/60)+'分';if(d<86400)return Math.floor(d/3600)+'时';return Math.floor(d/86400)+'天';};
function setConn(s){const el=$('conn');if(el)el.className=s;}

/* ---------- 消息渲染（官方风格：who 标签 + 内容；AI 侧带轻量 Markdown） ---------- */
function mdRender(src){
 let out='';const lines=String(src).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').split('\n');
 let inCode=false,codeBuf=[],tableBuf=[];
 const inline=s=>s.replace(/\*\*(.+?)\*\*/g,'<b>$1</b>').replace(/`([^`]+)`/g,'<code>$1</code>');
 const flushTable=()=>{ if(!tableBuf.length)return;
  const rows=tableBuf.filter(r=>!/^\s*\|?[\s:|-]*-[\s:|-]*\|?\s*$/.test(r));
  out+='<table class="md">'+rows.map((r,ri)=>'<tr>'+r.replace(/^\s*\|/,'').replace(/\|\s*$/,'').split('|')
   .map(c=>(ri===0?'<th>':'<td>')+inline(c.trim())+(ri===0?'</th>':'</td>')).join('')+'</tr>').join('')+'</table>';
  tableBuf=[];};
 for(const ln of lines){
  if(ln.trim().startsWith('```')){flushTable();
   if(inCode){out+='<pre class="code">'+codeBuf.join('\n')+'</pre>';codeBuf=[];inCode=false;}else inCode=true;
   continue;}
  if(inCode){codeBuf.push(ln);continue;}
  if(/^\s*\|/.test(ln)){tableBuf.push(ln);continue;}
  flushTable();
  if(/^#{1,4}\s/.test(ln)) out+='<h3>'+inline(ln.replace(/^#{1,4}\s/,''))+'</h3>';
  else if(/^\s*[-*]\s/.test(ln)) out+='<div class="li">• '+inline(ln.replace(/^\s*[-*]\s/,''))+'</div>';
  else out+=inline(ln)+'<br>';
 }
 flushTable(); if(inCode) out+='<pre class="code">'+codeBuf.join('\n')+'</pre>';
 return out;
}
let curTxt='';
function add(kind,t){
  if(kind==='err'){const d=document.createElement('div');d.className='err';d.textContent=t;log.appendChild(d);log.scrollTop=log.scrollHeight;return d;}
  if(kind==='notice'){const d=document.createElement('div');d.className='notice';d.textContent=t;log.appendChild(d);log.scrollTop=log.scrollHeight;return d;}
  const w=document.createElement('div');w.className='msg '+kind;
  const who=document.createElement('div');who.className='who';who.textContent=kind==='user'?'You':'opencode';
  const txt=document.createElement('div');txt.className='txt';
  if(kind==='ai'){txt.innerHTML=mdRender(t||'');curTxt=t||'';}else txt.textContent=t||'';
  w.append(who,txt);log.appendChild(w);log.scrollTop=log.scrollHeight;return txt;
}
function addMsgImgs(container,imgs){if(!imgs||!imgs.forEach||!imgs.length)return;const w=document.createElement('div');w.className='msg-thumbs';imgs.forEach(u=>{const im=document.createElement('img');im.className='msg-img';im.src=u;im.onclick=()=>openLightbox(u);w.appendChild(im);});container.parentNode.appendChild(w);log.scrollTop=log.scrollHeight;}
/* ---------- 图片放大（lightbox） ---------- */
function openLightbox(u){const lb=$('lightbox');if(!lb)return;lb.querySelector('img').src=u;lb.classList.add('on');}
$('lightbox').onclick=()=>$('lightbox').classList.remove('on');
function renderMsg(x){
  // 历史里桥把工具输出折成「> 🔧 工具名: …」引用行——基本无用，整行滤掉；滤完整条为空就不渲染
  const raw=String(x.text||'');
  const t=raw.split('\n').filter(l=>!l.startsWith('> 🔧')).join('\n').replace(/\n{3,}/g,'\n\n');
  if(!t.trim()&&!(x.images&&x.images.length))return null;
  const txt=add(x.role==='user'?'user':'ai',t);
  addMsgImgs(txt,x.images);
  return txt;
}
/* ---------- 内联工具卡片（可展开） ---------- */
let lastTool=null,lastToolKey='';
function toolCard(name,args,result){
  const key=name+'|'+JSON.stringify(args||{});
  let card=null;
  // start 与 result 都要复用同一张卡（start 建卡、result 原地更新为 ✓）；
  // key 不一致（如 result 帧参数更全）时退而找页面上最后一张同名未完成卡，避免一工具两行
  if(lastTool&&lastToolKey===key){card=lastTool;}
  if(!card&&result!=null){
    const pend=[...log.querySelectorAll('.tool .st.run')].pop();
    const cardEl=pend&&pend.closest('.tool');
    if(cardEl&&cardEl.querySelector('.nm').textContent===name)card=cardEl;
  }
  if(!card){
    card=document.createElement('div');card.className='tool';
    const head=document.createElement('div');head.className='t-head';
    const st=document.createElement('span');st.className='st run';st.textContent='◐';
    const nm=document.createElement('span');nm.className='nm';nm.textContent=name;
    const ar=document.createElement('span');ar.className='ar';
    head.append(st,nm,ar);
    const body=document.createElement('div');body.className='t-body';
    head.onclick=()=>card.classList.toggle('open');
    card.append(head,body);log.appendChild(card);
  }
  const head=card.querySelector('.t-head'),st=head.querySelector('.st'),ar=head.querySelector('.ar'),body=card.querySelector('.t-body');
  ar.textContent=toolArgs(args);
  if(result!=null){
    st.className='st ok';st.textContent='✓';
    const out=String(result);body.textContent=out.length>1200?out.slice(0,600)+'\n…\n'+out.slice(-500):out;
    lastTool=null;lastToolKey='';
  }else{
    st.className='st run';st.textContent='◐';
    lastTool=card;lastToolKey=key;
  }
  log.scrollTop=log.scrollHeight;return card;
}
function toolArgs(a){if(!a||typeof a!=='object')return '';
  const K=['path','file','cmd','command','pattern','query','glob','url','dir','description'];const p=[];
  for(const k of K){if(a[k]!=null&&a[k]!=='')p.push(k+'='+String(a[k]).slice(0,100));}
  if(!p.length){try{const s=JSON.stringify(a);if(s&&s!=='{}')p.push(s.slice(0,100));}catch(e){}}
  return p.join('  ');}

/* ---------- 连接 ---------- */
function connect(){
 ws=new WebSocket(WS);
 ws.onmessage=e=>{let m;try{m=JSON.parse(e.data)}catch(_){return}
  if(m.rid&&pend[m.rid]){pend[m.rid](m);delete pend[m.rid];return}
  if(m.type==='permission_request'){permId=m.id;permSid=m.sessionID;const f=m.metadata&&m.metadata.filepath?(' '+m.metadata.filepath):'';const pats=(m.patterns&&m.patterns.length)?(' '+m.patterns.join(' ')):'';$('permTxt').textContent='权限请求 · '+(m.permission||'tool')+f+pats;$('permBar').style.display='flex';return;}
  if(m.sessionId&&sid&&m.sessionId!==sid)return;
  if(m.type==='user_message'){
   // 桥可能替换了会话（手机端 sid 失效/为空时自动新建）：回显带回真实 id，立即对齐
   if(m.session && m.session!==sid){ sid=m.session; log.innerHTML=''; cur=null; curTxt=''; renderSess(); }
   setSessName();const t=add('user',m.text);addMsgImgs(t,m.images);cur=null;}
  else if(m.type==='text'){if(!cur){cur=add('ai','');curTxt='';}curTxt+=m.delta;cur.innerHTML=mdRender(curTxt);log.scrollTop=log.scrollHeight;}
  else if(m.type==='tool'){add('notice',m.delta);}
  else if(m.type==='error'){add('err',m.delta);cur=null;}
  else if(m.type==='run:started'){runs.add(m.sessionId);mark(m.sessionId,true);badge();if(m.sessionId===sid){steps=[];round=0;planned=false;renderSteps();}}
  else if(m.type==='run:finished'){runs.delete(m.sessionId);mark(m.sessionId,false);badge();cur=null;if(m.sessionId===sid){steps=[];renderSteps();}}
  else if(m.type==='round'){round=+m.n;renderSteps();}
  else if(m.type==='tool_start'){
    // 工具调用不进聊天流（会把回复糊掉）：进度只在顶部「任务 N/M」步骤栏展示
    if(!runs.has(sid)){renderSteps();return;}
    if(m.name==='todo_write'){const ts=(m.args&&m.args.todos)||[];steps=ts.slice(0,20).map((t,i)=>({name:'todo',title:String((t&&t.title)||('步骤 '+(i+1))).slice(0,80),status:(t&&t.status)==='completed'?'done':(t&&t.status)==='in_progress'?'run':'wait'}));planned=true;}
    else if(!planned){const d=m.args||{};const hint=String(d.description||d.command||d.cmd||d.path||d.file||d.pattern||'').slice(0,48);steps.push({name:m.name,title:m.name+(hint?' · '+hint:''),status:'run'});}
    renderSteps();
  }
  else if(m.type==='tool_result'){if(!runs.has(sid)){renderSteps();return;}if(m.name!=='todo_write'&&!planned){for(let i=steps.length-1;i>=0;i--){if(steps[i].name===m.name&&steps[i].status==='run'){steps[i].status='done';break;}}}renderSteps();}
  else if(m.type==='tool_denied'){if(!runs.has(sid)){renderSteps();return;}for(let i=steps.length-1;i>=0;i--){if(steps[i].name===m.name&&steps[i].status==='run'){steps[i].status='deny';break;}}renderSteps();}
  else if(m.type==='todo'){if(m.sessionId===sid){steps=(m.todos||[]).slice(0,20).map(t=>({name:'todo',title:String(t.content||'').slice(0,80),status:t.status==='completed'?'done':t.status==='in_progress'?'run':t.status==='cancelled'?'deny':'wait'}));planned=true;round=round||1;renderSteps();}}
  else if(m.type==='model:changed'){loadModels();}
  else if(m.type==='permission:changed'){try{$('modeSel').value=m.value}catch(e){}}
  else if(m.type==='sessions:changed'){loadState();}
  else if(m.type==='session:opened'){openS(m.id,false);}
  else if(m.type==='command_result'){add('notice','⌘ /'+m.command+' 已执行');}
  else if(m.type==='diff'){add('ai',m.delta);}
  else if(m.type==='question_done'){if(permQ&&permQ.id===m.id){permQ=null;$('qBar').style.display='none';}}
  else if(m.type==='question_request'){showQ(m);}
  else if(m.type==='fs'){fsSet(!!m.enabled,!!m.safe);}              // 电脑端切换文件开关
  else if(m.type==='hello'){
    fsSet(!!m.fs_enabled,!!m.fs_safe);     // PC (重)连时同步开关
    loadState();loadModels();              // 桥重连成功 → 刷新数据
  }
  // fs_read / fs_list 响应由 req()/pend 系统处理，不在 onmessage 分发（避免循环调用）
 };
 ws.onopen=()=>{setConn('live');loadState();loadModels();loadCommands();if(sid)openS(sid,false);};
 ws.onclose=(e)=>{let why='';const c=e&&e.code;if(c&&c!==1000)why=' (code '+c+')';setConn('dead');add('err','连接断开'+why+'，3 秒后重连…');setTimeout(()=>{log.innerHTML='';cur=null;connect();},3000);};
}
function req(o){return new Promise((res,rej)=>{
 const id=++rid;pend[id]=res;
 setTimeout(()=>{if(pend[id]){delete pend[id];rej(new Error('超时（连接不稳）'));}},9000);
 try{ws.send(JSON.stringify(Object.assign(o,{rid:id})));}catch(e){delete pend[id];rej(e);}
});}
function wsSend(o){if(!ws||ws.readyState!==1){add('err','连接未就绪，请等 1-2 秒（重连中）再发');return false;}ws.send(JSON.stringify(o));return true;}

/* ---------- 状态与会话 ---------- */
// Windows 桥的 state.workspace 可能是正斜杠（D:/x）而 sessions[].workspace 是原生反斜杠（D:\x），
// 两边直接字符串比较会分成两个组 → 列表全空、下拉 (0)。所有 workspace 比较/分组前统一成正斜杠。
const normWs=w=>String(w||'').replace(/\\/g,'/');
function applyState(s){
 fsSet(!!s.fs,!!s.fs_safe);
 ALL=s.sessions||[];
 const map={};ALL.forEach(x=>{const k=normWs(x.workspace);(map[k]=map[k]||{n:0,u:0}).n++;map[k].u=Math.max(map[k].u,x.updated)});
 const w0=normWs(s.workspace);if(w0&&!map[w0])map[w0]={n:0,u:0};
 const sel=$('proj');
 if(w0&&window.__ws&&w0!==window.__ws)projPin=false;
 const keep=(projPin&&wsCur)?wsCur:(w0||wsCur);
 const opts=Object.keys(map).sort((a,b)=>map[b].u-map[a].u).map(w=>'<option value="'+esc(w)+'">'+esc(w.split('/').filter(Boolean).pop()||w)+' ('+map[w].n+')</option>').join('');
 if(opts!==window.__projOpts){window.__projOpts=opts;sel.innerHTML=opts;}
 wsCur=map[keep]?keep:(sel.options[0]?sel.options[0].value:'');sel.value=wsCur;
 window.__ws=w0;window.__branch=s.branch||'';
 runs=new Set(ALL.filter(x=>x.running).map(x=>x.id));
 renderSteps();
 try{$('modeSel').value=s.mode||'always'}catch(e){}
 try{$('ver').textContent=s.version?('v'+s.version):''}catch(e){}
 const _cur=s.current||'';if(_cur&&_cur!==lastCurrent){lastCurrent=_cur;loadModels();}
 const _cs=s.current_session||'';if(_cs&&_cs!==sid&&!cur){openS(_cs,false);}
 // 流式渲染中（cur 存在）不自动跳转/清屏，防止把刚发出的回显和回复洗掉
 if(!sid&&!cur){const cand=ALL.filter(x=>normWs(x.workspace)===wsCur).sort((a,b)=>b.updated-a.updated)[0];if(cand)openS(cand.id,false);}
 setSessName();renderSess();badge();
 fsSyncAvail();
}
function loadState(){req({type:'state'}).then(applyState).catch(()=>{});}
let steps=[],round=0,planned=false;
function renderSteps(){const el=$('steps');if(!el)return;if(!steps.length||!runs.has(sid)){el.classList.remove('show');return;}el.classList.add('show');
 const done=steps.filter(x=>x.status==='done').length;
 el.innerHTML='<div class="stepsbar-head">任务 '+done+'/'+steps.length+' · 运行中'+(round>0?(' · 第 '+round+' 轮'):'')+'</div>'+steps.map(x=>'<div class="step '+x.status+'">'+(x.status==='done'?'✓':x.status==='run'?'●':x.status==='deny'?'✗':'○')+' '+esc(x.title)+'</div>').join('');
 el.scrollTop=el.scrollHeight;}
function renderSess(){
 const rows=ALL.filter(x=>normWs(x.workspace)===wsCur).sort((a,b)=>b.updated-a.updated);
 let h='';
 rows.forEach(x=>{h+='<div class="s'+(x.id===sid?' on':'')+'" data-id="'+x.id+'">'
  +(x.running?'<span class="run">▶</span>':'')+'<span class="t">'+esc(x.title||'（无标题）')+'</span>'
  +'<span class="s-act" data-act="rename">✎</span><span class="s-act del" data-act="del">🗑</span>'
  +'<span class="ago">'+ago(x.updated)+'</span></div>';});
 $('sessList').innerHTML=h||'<div class="side-grp" style="padding:8px">该项目暂无会话</div>';
}
function badge(){const c=$('conn');if(c)c.classList.toggle('working',runs.size>0);document.body.classList.toggle('working',runs.size>0);$('run').textContent=runs.size?'▶ '+runs.size:'';$('btnStop').classList.toggle('on',runs.has(sid));renderRuns();}
function mark(id,on){ALL.forEach(x=>{if(x.id===id)x.running=on});renderSess();}
function setSessName(){const el=$('sessName');if(!el)return;const i=ALL.findIndex(x=>x.id===sid);const t=i>=0?ALL[i].title:'';const w=i>=0?normWs(ALL[i].workspace).split('/').filter(Boolean).pop():'';
 el.textContent=t?('会话：'+t+(w?'  ·  '+w:'')+(window.__branch?('  ⎇ '+window.__branch):'')):'';}

/* ---------- 运行中横幅 ---------- */
function renderRuns(){const bar=$('runsBar');if(!bar)return;
 const list=ALL.filter(x=>x.running);
 if(!list.length){bar.classList.remove('on');bar.innerHTML='';return;}
 bar.classList.add('on');
 bar.innerHTML=list.map(x=>'<span class="rr'+(x.id===sid?' cur':'')+'" data-id="'+x.id+'">▶ '+esc((x.title||'（无标题）').slice(0,24))+'</span>').join('');}
$('runsBar').onclick=e=>{const c=e.target.closest('.rr');if(!c)return;projPin=false;openS(c.dataset.id,true);};

/* ---------- 会话打开 / 新建 ---------- */
async function openS(id,notify){
 const seq=++openSeq;
 if(!notify){const sx=ALL.find(x=>x.id===id);syncProj(normWs(window.__ws||(sx&&sx.workspace)||''));}
 sid=id;closeSide();fsSyncAvail();
 log.innerHTML='';cur=null;renderSess();setSessName();badge();
 if(notify){try{ws.send(JSON.stringify({type:'open_session',id}))}catch(e){}}
 try{
  const m=await req({type:'messages',id});
  if(seq!==openSeq)return;
  if(cur)return;
  if(m.messages&&m.messages.length)m.messages.forEach(renderMsg);
  else log.innerHTML='<div class="empty">（空会话，直接发第一条消息）</div>';
 }catch(e){
  if(seq!==openSeq)return;
  add('err','加载历史失败：'+e.message+'，点击重试');
  log.onclick=()=>{log.onclick=null;openS(id,notify);};
 }
}
function syncProj(w){w=w||'';if(!w||w===wsCur)return;wsCur=w;const sel=$('proj');for(const o of sel.options){if(o.value===w){sel.value=w;break;}}renderSess();}
$('sessNew').onclick=async()=>{try{const m=await req({type:'new_session',dir:wsCur||window.__ws});if(m&&m.error)add('err',m.error);}catch(e){add('err','新建失败');}loadState();};
$('sessList').onclick=e=>{const act=e.target.closest('.s-act');
 if(act){const row=act.closest('.s');if(!row)return;const id=row.dataset.id;
  if(act.dataset.act==='del'){ if(confirm('删除该会话？')) req({type:'delete_session',id}).then(()=>loadState()); }
  else if(act.dataset.act==='rename'){ const sx=ALL.find(x=>x.id===id); rnOpen(id,(sx&&sx.title)||''); }
  return; }
 const r=e.target.closest('.s');if(r)openS(r.dataset.id,true);};

/* ---------- 侧栏开关（手机抽屉） ---------- */
function closeSide(){$('side').classList.remove('open');$('sideMask').classList.remove('open');}
$('btnS').onclick=()=>{$('side').classList.toggle('open');$('sideMask').classList.toggle('open');if($('side').classList.contains('open'))loadState();};
$('sideMask').onclick=closeSide;

/* ---------- 模型 / 权限 / 项目 ---------- */
async function loadModels(){
 let d; try{ d=await req({type:'models'});}catch(e){d=null;}
 const sel=$('model');
 if(!d||!d.models||!d.models.length){ sel.innerHTML='<option value="">连接中…</option>'; return; }
 sel.innerHTML=d.models.map(x=>'<option value="'+esc(x.key)+'"'+(x.is_current?' selected':'')+'>'+esc(x.model_id||x.display_name)+(x.vision?' 👁':'')+(x.reasoning?' 🧠':'')+'</option>').join('');
}
$('model').onchange=async()=>{const k=$('model').value;try{await req({type:'model',key:k});}catch(e){}loadModels();};
$('modeSel').onchange=async()=>{const v=$('modeSel').value;try{await req({type:'mode',value:v});}catch(e){}};
$('proj').onchange=()=>{wsCur=$('proj').value;projPin=true;renderSess();
 try{req({type:'workspace',dir:wsCur}).catch(()=>{});}catch(e){}};

/* ---------- 权限 / 提问审批 ---------- */
$('permBar').onclick=e=>{const b=e.target.closest('button');if(!b||!permId)return;const v=b.dataset.p;try{ws.send(JSON.stringify({type:'permission_response',id:permId,response:v,sessionID:permSid}));}catch(_){}permId=null;$('permBar').style.display='none';};
let permQ=null;
function showQ(m){
 const qs=m.questions||[];
 permQ={id:m.id,questions:qs,answers:[]};
 $('qTxt').textContent=qs.length>1?('opencode 提问（'+qs.length+' 个，逐个作答）'):((qs[0]&&qs[0].header)||'opencode 提问');
 const box=$('qOpts');
 box.innerHTML=qs.map((q,i)=>{
  const opts=(q.options||[]).map(o=>'<button type="button" class="q-opt" data-i="'+i+'" data-l="'+esc(o.label||'')+'">'+esc(o.label||'')+'</button>').join('');
  const custom=q.custom?'<button type="button" class="q-opt" data-i="'+i+'" data-l="__custom__">✎ 自定义</button>':'';
  return '<div class="q-item"><div class="q-q">'+esc(((i+1)+'. '+(q.question||'')).slice(0,120))+'</div><div class="q-row">'+opts+custom+'</div></div>';
 }).join('')+'<div class="q-row"><button type="button" class="q-opt q-deny" id="qReject">拒绝回答</button></div>';
 box.onclick=e=>{const b=e.target.closest('.q-opt');if(!b||!permQ)return;
  if(b.id==='qReject'){try{ws.send(JSON.stringify({type:'question_reject',id:permQ.id}))}catch(_){}permQ=null;$('qBar').style.display='none';return;}
  const i=+b.dataset.i;let label=b.dataset.l;
  if(label==='__custom__'){const t=prompt((qs[i]&&qs[i].question)||'你的回答');if(!t)return;label=t;}
  permQ.answers[i]=[label];b.classList.add('picked');
  if(permQ.answers.filter(Boolean).length>=qs.length){
   try{ws.send(JSON.stringify({type:'question_reply',id:permQ.id,answers:permQ.answers}))}catch(_){}
   add('user','（回答：'+permQ.answers.map(a=>(a||[]).join('/')).join('；')+'）');
   permQ=null;$('qBar').style.display='none';
  }
 };
 $('qBar').style.display='block';
}
$('btnStop').onclick=()=>{if(sid)try{ws.send(JSON.stringify({type:'stop',session:sid}))}catch(_){}};

/* ---------- 真实命令（斜杠菜单） ---------- */
let REAL=[];
async function loadCommands(){try{const d=await req({type:'commands'});if(d&&d.commands)REAL=d.commands||[];}catch(e){}}
function slashShow(q){
 const box=$('slash');q=(q||'').toLowerCase();
 const items=[['/file','选择文件/图片，传给PC识别']].concat(
  REAL.map(c=>['/'+c.name,(c.description||'')+(c.agent?(' · agent:'+c.agent):'')+(c.source&&c.source!=='command'?(' · '+c.source):'')]));
 const list=items.filter(c=>!q||c[0].slice(1).toLowerCase().includes(q)).slice(0,9);
 if(!list.length){box.classList.remove('open');return;}
 box.innerHTML=list.map(c=>'<div class="si" data-k="'+esc(c[0])+'"><span class="k">'+esc(c[0])+'</span><span class="d">'+esc(c[1])+'</span></div>').join('');
 box.classList.add('open');
}
 $('i').addEventListener('input',()=>{const v=$('i').value;if(v.startsWith('/'))slashShow(v.slice(1));else $('slash').classList.remove('open');});

/* ---------- @ 文件/目录引用（Cursor 式） ---------- */
let atFiles=[],atOpen=false;
function atShow(q){
 const box=$('atPick');if(!box)return;
 atOpen=true;
 const term=(q||'').toLowerCase();
 let items=atFiles;
 if(term)items=atFiles.filter(f=>f.name.toLowerCase().includes(term)||f.path.toLowerCase().includes(term));
 if(!items.length){box.innerHTML='<div class="at-empty">（无匹配文件）</div>';box.classList.add('open');return;}
 box.innerHTML=items.slice(0,15).map(f=>'<div class="at-i" data-p="'+esc(f.path)+'" data-n="'+esc(f.name)+'"><span class="at-ic">'+(f.dir?'📁':'📄')+'</span><span class="at-nm">'+esc(f.name)+'</span><span class="at-p">'+esc(f.path)+'</span></div>').join('');
 box.classList.add('open');
}
function atHide(){atOpen=false;const box=$('atPick');if(box)box.classList.remove('open');}
async function atFetch(p){
 try{ const m=await req({type:'fs_list',path:p||window.__ws||''});
  if(m.error||!m.fs)return;
  atFiles=[];
  (m.dirs||[]).forEach(d=>atFiles.push({name:d.name,path:d.path,dir:true}));
  (m.files||[]).forEach(f=>atFiles.push({name:f.name,path:f.path,dir:false}));
 }catch(e){}
}
$('i').addEventListener('input',()=>{
 const v=$('i').value;
 // @ 触发：光标前最后一个 @ 后面的文本作为搜索词
 const atIdx=v.lastIndexOf('@');
 if(atIdx>=0&&(atIdx===0||v[atIdx-1]===' '||v[atIdx-1]==='\n')){
  const q=v.slice(atIdx+1);
  if(q.length<=60&&!q.includes(' ')){
   if(!atFiles.length||!atOpen)atFetch(window.__ws);
   atShow(q);return;
  }
 }
 atHide();
});
$('atPick').onclick=e=>{const it=e.target.closest('.at-i');if(!it)return;
 const p=it.dataset.p,n=it.dataset.n;atHide();
 const i=$('i');const v=i.value;const atIdx=v.lastIndexOf('@');
 if(atIdx>=0)i.value=v.slice(0,atIdx);
 // 添加为文件引用
 const rr={display:n,path:p};rr._uid=++attSeq;fsPendRefs.push(rr);fsAddRefChip(rr);
 i.focus();};
document.addEventListener('click',e=>{if(atOpen&&!e.target.closest('#atPick')&&!e.target.closest('#i'))atHide();});
$('i').addEventListener('keydown',e=>{if(e.key==='Escape'&&atOpen){atHide();}});
$('slash').addEventListener('click',e=>{const si=e.target.closest('.si');if(!si)return;const k=si.dataset.k;
 if(k==='/file'){const i=$('i');i.value='';try{$('fileAny').click()}catch(_){}
 }else{const i=$('i');i.value=k+' ';i.focus();}
 $('slash').classList.remove('open');});
$('i').addEventListener('keydown',e=>{if(e.key==='Escape')$('slash').classList.remove('open');});

/* ---------- 发送 ---------- */
$('f').onsubmit=ev=>{ev.preventDefault();
 const i=$('i');const t=i.value.trim();
 if(t.startsWith('/file')){ i.value='';cur=null;try{$('fileAny').click()}catch(_){}; $('slash').classList.remove('open'); return; }
 if(!t&&!pendingAtts.length&&!fsPendRefs.length)return;
 const mc=t.match(/^\/([a-z0-9_:-]+)(\s+(.*))?$/i);
 if(mc){ i.value='';cur=null;
  wsSend({type:'command',session:sid,command:mc[1],arguments:(mc[3]||'').trim(),atts:pendingAtts});
  pendingAtts=[];try{$('pendingAtts').innerHTML=''}catch(_){}; fsPendRefs=[]; return; }
 i.value='';cur=null;
 wsSend({type:'send',session:sid,text:t,atts:pendingAtts,refs:(fsPendRefs.length?fsPendRefs:null)});
 pendingAtts=[];try{$('pendingAtts').innerHTML=''}catch(_){}; fsPendRefs=[];
};

/* ---------- 附件（按钮 / 选择 / 粘贴 / 拖放） ---------- */
$('btnAtt').onclick=()=>{try{$('fileAny').click()}catch(_){}};
let pendingAtts=[];let attSeq=0;
function addAttChip(a){const box=$('pendingAtts');if(!box)return;const c=document.createElement('div');c.className='att-chip';c.dataset.uid=a._uid||'';
 if((a.data||'').startsWith('data:image')){const im=document.createElement('img');im.src=a.data;c.appendChild(im);}
 const sp=document.createElement('span');sp.textContent=a.name;c.appendChild(sp);
 const x=document.createElement('span');x.className='chip-x';x.textContent='✕';x.title='移除';
 x.onclick=()=>{ if(a._uid){pendingAtts=pendingAtts.filter(v=>v._uid!==a._uid);} c.remove(); };
 c.appendChild(x);box.appendChild(c);}
function readAsImage(file,cb){const r=new FileReader();r.onload=()=>{const img=new Image();img.onload=()=>{const max=1280;let w=img.width,h=img.height;if(w>max||h>max){if(w>=h){h=Math.round(h*max/w);w=max;}else{w=Math.round(w*max/h);h=max;}}const c=document.createElement('canvas');c.width=w;c.height=h;c.getContext('2d').drawImage(img,0,0,w,h);cb(c.toDataURL('image/jpeg',0.8));};img.src=r.result;};r.readAsDataURL(file);}
function readAsData(file,cb){const r=new FileReader();r.onload=()=>cb(r.result);r.readAsDataURL(file);}
$('fileAny').addEventListener('change',e=>{const f=e.target.files[0];if(!f)return;const isImg=(f.type||'').startsWith('image/');const cb=d=>{const it={name:f.name,data:d,_uid:++attSeq};pendingAtts.push(it);addAttChip(it);};if(isImg)readAsImage(f,cb);else readAsData(f,cb);e.target.value='';});
function addPasted(f){if(!f)return;const isImg=(f.type||'').startsWith('image/');const cb=d=>{const it={name:f.name||'pasted',data:d,_uid:++attSeq};pendingAtts.push(it);addAttChip(it);};if(isImg)readAsImage(f,cb);else readAsData(f,cb);}
$('i').addEventListener('paste',e=>{const items=(e.clipboardData&&e.clipboardData.items)||[];const it=[].slice.call(items).find(x=>x.type&&x.type.startsWith('image/'));if(it){e.preventDefault();addPasted(it.getAsFile());}});
document.addEventListener('dragover',e=>e.preventDefault());
document.addEventListener('drop',e=>{e.preventDefault();const fs=(e.dataTransfer&&e.dataTransfer.files)||[];[].slice.call(fs).forEach(addPasted);});

/* ---------- WEB 文件浏览 / 编辑（电脑端开关 fs_enabled 门控） ---------- */
let fsOn=false, fsSafeMode=true, fsDir='', fsViewPath='', fsViewRaw='', fsEditing=false, fsEdittable=false;
const fsMaxEdit=200*1024; // >200KB 只读（手机浏览器性能）
const $fs=id=>document.getElementById(id);
function fsSet(on,safe){ fsOn=on; if(safe!=null)fsSafeMode=safe;
 const e=$('fsEntry'); if(e)e.style.display=on?'':'none';
 const t=$('fsPanelTitle'); if(t)t.textContent=on?('📁 文件'+(fsSafeMode?' 🔒 安全目录（仅当前项目）':' ⚠ 任意路径')):'📁 文件';
 if(!on){$('fsPanel').classList.remove('on');$('fsView').classList.remove('on');} }
// 按桥上报的 fs 开关重同步文件入口可见性（openS/applyState 调用；fsSet 只在 hello/state 触发过一次，
// 页面后续任何时序都不应再依赖它，故单独提供这个幂等的再同步）
function fsSyncAvail(){ const e=$('fsEntry'); if(e)e.style.display=fsOn?'':'none'; }
function fsHuman(n){ if(n>=1048576)return (n/1048576).toFixed(1)+'M'; if(n>=1024)return (n/1024).toFixed(1)+'K'; return n+'B'; }
// 按扩展名给文件类型着色（列表小圆点 + 文件名）
const fsExtColor={js:'#e5c07b',mjs:'#e5c07b',ts:'#3178c6',tsx:'#3178c6',jsx:'#e5c07b',py:'#3572A5',go:'#00ADD8',rs:'#dea584',
 html:'#e34c26',htm:'#e34c26',vue:'#41b883',css:'#563d7c',scss:'#c6538c',json:'#98c379',md:'#98c379',yml:'#cb171e',yaml:'#cb171e',
 sh:'#89e051',bat:'#c1f12e',ps1:'#4f99e0',psm1:'#4f99e0',sql:'#e38c00',java:'#b07219',c:'#555555',h:'#555555',cpp:'#f34b7d',cs:'#178600',rb:'#701516',php:'#4F5D95',swift:'#F05138'};
function fsColor(name){ const m=(name||'').split('.').pop().toLowerCase(); return fsExtColor[m]||'#8a93a5'; }
function fsOpenPanel(){ $('fsPanel').classList.add('on'); if(!fsDir)fsNav(''); }
$('btnFiles').onclick=fsOpenPanel;
$('fsClose').onclick=()=>$('fsPanel').classList.remove('on');
$('fsGo').onclick=()=>fsNav($('fsPath').value.trim());
$('fsPath').addEventListener('keydown',e=>{ if(e.key==='Enter')fsNav($('fsPath').value.trim()); });
// 面包屑：把路径按 / \ 切段，逐段可点击回跳
function fsCrumb(p){
 const el=$('fsCrumb'); if(!p){el.innerHTML='<span class="fs-c on">此电脑（磁盘列表）</span>';return;}
 const segs=String(p).split(/[\\/]/).filter(Boolean);
 let acc='',h='<span class="fs-c" data-p="">此电脑</span>';
 if(/^[A-Za-z]:$/.test(segs[0]))acc=segs[0]+'\\';
 segs.forEach((s,i)=>{ if(i===0&&/^[A-Za-z]:$/.test(s)){h+=' <span class="fs-c on">'+esc(s)+'\\</span>';return;}
  acc=acc.endsWith('\\')||acc.endsWith('/')?acc+s:acc+'/'+s;
  h+=' <span class="fs-c'+(i===segs.length-1?' on':'')+'" data-p="'+esc(acc)+'">'+esc(s)+'</span>'; });
 el.innerHTML=h;
}
$('fsCrumb').onclick=e=>{const c=e.target.closest('.fs-c');if(c&&c.dataset.p!=null)fsNav(c.dataset.p);};
async function fsNav(p){
 p=(p||'').trim();
 try{
  const m=await req({type:'fs_list',path:p});
  if(m.error){fsToast(m.error);return;}
  fsDir=m.path||''; if(m.safe!=null)fsSafeMode=!!m.safe; $('fsPath').value=fsDir; fsCrumb(fsDir); renderFsList(m); fsSet(fsOn,fsSafeMode);
 }catch(e){ fsToast('读取失败：'+e.message); }
}
function renderFsList(m){
 const el=$('fsList'); let h='';
 if(fsDir){ // 上级目录：磁盘列表页无上级
  const parent=fsDir.replace(/[\\/]+$/,'').replace(/[\\/][^\\/]+$/,'')||''; // 逐级剥尾
  h+='<div class="fs-row dir" data-p="'+esc(parent)+'"><span class="fs-ic">↩︎</span><span class="fs-nm">..</span></div>';
 }
 (m.dirs||[]).forEach(d=>{ h+='<div class="fs-row dir" data-p="'+esc(d.path)+'"><span class="fs-ic">📁</span><span class="fs-nm">'+esc(d.name)+'</span><span class="fs-more" data-more="1">⋯</span></div>'; });
 (m.files||[]).forEach(f=>{ h+='<div class="fs-row file" data-p="'+esc(f.path)+'" data-sz="'+(f.size||0)+'"><span class="fs-ic" style="color:'+fsColor(f.name)+'">●</span><span class="fs-nm" style="color:'+fsColor(f.name)+'">'+esc(f.name)+'</span><span class="fs-sz">'+fsHuman(f.size||0)+'</span><span class="fs-more" data-more="1">⋯</span></div>'; });
 el.innerHTML=h||'<div class="empty">（空目录）</div>';
}
let fsmSuppress=false;
$('fsList').onclick=e=>{
 if(fsmSuppress){ fsmSuppress=false; return; }
 const more=e.target.closest('.fs-more');
 if(more){ const r=more.closest('.fs-row'); fsmShow(r.dataset.p, r.classList.contains('dir')); return; }
 const r=e.target.closest('.fs-row'); if(!r)return;
 if(r.classList.contains('dir')){ fsNav(r.dataset.p); return; }
 fsOpenFile(r.dataset.p, +r.dataset.sz||0);
};
// 长按 550ms 同样唤出操作菜单
let fsLpTimer=null;
$('fsList').addEventListener('touchstart',e=>{ const r=e.target.closest('.fs-row'); if(!r)return;
 fsLpTimer=setTimeout(()=>{ fsmSuppress=true; fsmShow(r.dataset.p, r.classList.contains('dir')); },550); },{passive:true});
['touchend','touchmove','touchcancel'].forEach(ev=>$('fsList').addEventListener(ev,()=>{ if(fsLpTimer){clearTimeout(fsLpTimer);fsLpTimer=null;} },{passive:true}));
// ---- 文件操作菜单（编辑/改名/删除） ----
let fsmPath='', fsmIsDir=false;
function fsmShow(path,isDir){ fsmPath=path; fsmIsDir=isDir;
 $('fsmName').textContent=path;
 $('fsmEdit').style.display=isDir?'none':'';
 $('fsMenu').classList.add('on'); }
$('fsMenu').onclick=e=>{
 if(e.target.id!=='fsMenu'&&e.target.tagName!=='BUTTON')return;
 const act=e.target.dataset&&e.target.dataset.act;
 if(!act&&e.target.id==='fsMenu')act='cancel';
 $('fsMenu').classList.remove('on');
 if(!act||act==='cancel')return;
 const name=fsmPath.split(BS).pop().split('/').pop();
 if(act==='chat'){ fsToChat('📎 '+fsmPath, {path:fsmPath}); return; }
 if(act==='edit'){ fsOpenFile(fsmPath,0); return; }
 if(act==='rename'){ const nn=prompt('新名称',name); if(!nn||nn===name)return;
  req({type:'fs_rename',path:fsmPath,name:nn}).then(m=>{ fsToast(m.error?('改名失败：'+m.error):('已改名 ✓')); fsNav(fsDir); }).catch(()=>fsToast('改名失败')); return; }
 if(act==='del'){ if(!confirm('确认删除 '+name+' ？'))return;
  req({type:'fs_delete',path:fsmPath}).then(m=>{ fsToast(m.error?('删除失败：'+m.error):('已删除 ✓')); fsNav(fsDir); }).catch(()=>fsToast('删除失败')); return; }
};
$('fsProj').onclick=async()=>{
 if(!fsDir){fsToast('先进入一个目录');return;}
 try{ await req({type:'workspace',dir:fsDir}); fsToast('已切换项目：'+fsDir); loadState(); }
 catch(e){ fsToast('切换失败：'+e.message); }
};
// 文件查看 / 编辑（直接进入编辑模式，带语法着色）
async function fsOpenFile(path,size){
 let m; try{ m=await req({type:'fs_read',path}); }catch(e){ fsToast('读取超时'); return; }
 if(m.error){ fsToast(m.error); return; }
 fsViewPath=m.path||path; fsViewRaw=m.content||'';
 fsEdittable=!m.truncated&&(m.size||size||0)<=fsMaxEdit;
 $('fvName').textContent=m.name||path.split(/[\\/]/).pop();
 $('fvMeta').textContent=(m.size?fsHuman(m.size):'')+(m.truncated?' · 已截断（>1MB 只读）':'')+(fsEdittable?'':' · 大文件只读');
 // 直接进入编辑模式（语法着色 + 保存按钮），不经过预览
 fsEditing=fsEdittable; fsRenderView(); $('fsView').classList.add('on');
}
const fsHlLangOf=name=>{const m=(name||'').split('.').pop().toLowerCase();
 const M={ps1:'powershell',psm1:'powershell',psd1:'powershell',js:'javascript',mjs:'javascript',jsx:'javascript',ts:'typescript',tsx:'typescript',py:'python',go:'go',rs:'rust',html:'xml',htm:'xml',vue:'xml',css:'css',scss:'scss',json:'json',md:'markdown',markdown:'markdown',yml:'yaml',yaml:'yaml',sh:'bash',bash:'bash',sql:'sql',java:'java',c:'c',h:'c',cpp:'cpp',cs:'csharp',rb:'ruby',php:'php',swift:'swift'};
 return M[m]||'';};
let fsHlTimer=null;
function fsHlEdit(){ // 编辑态实时着色：透明 textarea 叠在高亮层上
 const ta=$('fvTa'),pre=$('fvHl'),code=pre.querySelector('code'),txt=ta.value;
 try{
  if(window.hljs){
   let lang=fsHlLangOf(fsViewPath);
   if(!lang||!hljs.getLanguage(lang)) lang=(hljs.highlightAuto(txt).language)||'';
   if(lang&&hljs.getLanguage(lang)) code.innerHTML=hljs.highlight(txt,{language:lang,ignoreIllegals:true}).value;
   else code.textContent=txt;
  } else code.textContent=txt;
 }catch(_){ code.textContent=txt; }
 pre.scrollTop=ta.scrollTop; pre.scrollLeft=ta.scrollLeft;
}
function fsRenderView(){
 const pre=$('fvPre'),ta=$('fvTa'),wrap=$('fvEditWrap');
 $('fvEdit').style.display='none'; // 直接编辑模式，不再需要编辑按钮
 $('fvSave').style.display=fsEditing?'':'none';
 if(fsEditing){
  pre.style.display='none'; wrap.classList.add('on');
  ta.value=fsViewRaw; fsHlEdit(); ta.focus();
  return;
 }
 wrap.classList.remove('on'); pre.style.display='';
 const code=pre.querySelector('code');
 code.className='hljs'; code.textContent=fsViewRaw;
 try{ if(window.hljs){ const r=hljs.highlightAuto(fsViewRaw); code.className='hljs language-'+r.language; code.innerHTML=r.value; } }
 catch(_){ code.textContent=fsViewRaw; }
}
$('fvEdit').onclick=()=>{ fsEditing=true; fsRenderView(); };
let fsHlDebounce=null;
$('fvTa').addEventListener('input',()=>{ clearTimeout(fsHlDebounce); fsHlDebounce=setTimeout(fsHlEdit,80); });
$('fvTa').addEventListener('scroll',()=>{ const pre=$('fvHl'); pre.scrollTop=$('fvTa').scrollTop; pre.scrollLeft=$('fvTa').scrollLeft; });
$('fvTa').addEventListener('keydown',e=>{ if(e.key==='Tab'){ e.preventDefault(); const ta=e.target,s=ta.selectionStart,en=ta.selectionEnd; ta.value=ta.value.slice(0,s)+'  '+ta.value.slice(en); ta.selectionStart=ta.selectionEnd=s+2; fsHlEdit(); } });
$('fvSave').onclick=async()=>{
 try{
  const m=await req({type:'fs_write',path:fsViewPath,content:$('fvTa').value});
  if(m.error){ fsToast('保存失败：'+m.error); return; }
  fsViewRaw=$('fvTa').value; fsEditing=false; fsRenderView(); fsToast('已保存 ✓');
 }catch(e){ fsToast('保存失败：'+e.message); }
};
// ---- 加入对话框：文件引用 / 选中代码片段 → 聊天输入框 ----
const NL=String.fromCharCode(10),TICK=String.fromCharCode(96,96,96),BS=String.fromCharCode(92);
let fsPendRefs=[];
// Cursor 式引用：输入框只放短引用，实际内容作为 refs 附件由电脑端在发送时注入给模型
function fsToChat(display,ref){ const i=$('i');
 // 引用只进附件标签（Cursor 式），源码与路径都不再塞进输入框
 if(ref){ const rr=Object.assign({display},ref); rr._uid=++attSeq; fsPendRefs.push(rr); fsAddRefChip(rr); }
 fsCloseAllPopups(); $('fsMinName').textContent=display; fsMinimize(); i.focus();
 try{document.body.classList.remove('coding')}catch(_){}
 fsToast('已加入对话框，回车发送'); }

function fsAddRefChip(rr){ const box=$('pendingAtts'); if(!box)return;
 const c=document.createElement('div'); c.className='att-chip'; c.dataset.uid=rr._uid;
 c.textContent='📄 '+(rr.start?(rr.start+'-'+rr.end+'行 '):'')+rr.display;
 const x=document.createElement('span'); x.className='chip-x'; x.textContent='✕'; x.title='移除';
 x.onclick=()=>{ fsPendRefs=fsPendRefs.filter(v=>v._uid!==rr._uid); c.remove(); };
 c.appendChild(x); box.appendChild(c); }

function fsCloseAllPopups(){ // 加入对话框后关闭所有弹出页，只留最小化条
 const p=$('fsPanel'); if(p)p.classList.remove('on');
 const m=$('fsMenu'); if(m)m.classList.remove('on');
 document.body.classList.remove('dd-open');
 document.querySelectorAll('.dd-pop.open').forEach(q=>q.classList.remove('open')); }
$('fvChat').onclick=()=>{ fsToChat('📎 '+fsViewPath, {path:fsViewPath}); };
function fsUpdateSelBtn(){
 const ta=$('fvTa'),btn=$('fvSelChat'); if(!ta||!btn)return;
 if(!fsEditing||!$('fsView').classList.contains('on')){btn.style.display='none';return;}
 const s=ta.selectionStart,e=ta.selectionEnd;
 if(e>s){ const sl=ta.value.slice(0,s).split(NL).length; const el=sl+ta.value.slice(s,e).split(NL).length-1;
  btn.textContent='💬 加入对话框（第 '+sl+'-'+el+' 行）'; btn.style.display=''; }
 else btn.style.display='none';
}
['keyup','mouseup','touchend','input'].forEach(ev=>$('fvTa').addEventListener(ev,fsUpdateSelBtn));
$('fvSelChat').onclick=()=>{
 const ta=$('fvTa'),s=ta.selectionStart,e=ta.selectionEnd; if(e<=s)return;
 const sl=ta.value.slice(0,s).split(NL).length; const el=sl+ta.value.slice(s,e).split(NL).length-1;
 const name=fsViewPath.split(BS).pop().split('/').pop();
 fsToChat('📎 '+name+':'+sl+'-'+el, {path:fsViewPath,start:sl,end:el});
};
$('fvDesktop').onclick=()=>{ try{ wsSend({type:'fs_open_desktop',path:fsViewPath}); fsToast('已在电脑端打开'); }catch(e){} };
function fsMinimize(){ $('fsView').classList.remove('on'); $('fsMinBar').classList.add('on'); }
function fsRestore(){ $('fsMinBar').classList.remove('on'); $('fsView').classList.add('on');
 if(fsEditing){ fsUpdateSelBtn(); const ta=$('fvTa'); if(ta)ta.focus(); } }
$('fsMinBar').onclick=fsRestore;
$('fvMin').onclick=()=>{ $('fsMinName').textContent=$('fvName').textContent; fsMinimize(); };
$('fvClose').onclick=()=>{ if(fsEditing&&!confirm('正在编辑中，关闭将丢弃未保存的修改？'))return;
 fsEditing=false; $('fsMinBar').classList.remove('on'); $('fsView').classList.remove('on'); };
// 轻量 toast（复用日志 err 样式，不打断）
let fsToastTimer=null;
function fsToast(t){ const el=$('fsHint'); el.textContent=t; el.style.color='#e5484d';
 clearTimeout(fsToastTimer); fsToastTimer=setTimeout(()=>{el.textContent='';},4000); }

connect();setInterval(loadState,8000);

// ---- 重命名会话弹窗：预填当前标题，校验空名/同项目重名 ----
let rnId='';
function rnOpen(id,cur){ rnId=id; $('rnInput').value=cur; $('rnErr').textContent='';
 $('rnMask').classList.add('on'); setTimeout(()=>{ $('rnInput').focus(); $('rnInput').select(); },60); }
function rnClose(){ $('rnMask').classList.remove('on'); }
function rnSubmit(){
 const v=$('rnInput').value.trim();
 if(!v){ $('rnErr').textContent='名称不能为空'; return; }
 const sx=ALL.find(x=>x.id===rnId); const ws=normWs((sx&&sx.workspace)||'');
 if(ALL.some(x=>x.id!==rnId&&normWs(x.workspace)===ws&&(x.title||'').trim()===v)){
  $('rnErr').textContent='该项目下已有同名会话'; return; }
 req({type:'rename_session',id:rnId,title:v}).then(()=>{ rnClose(); loadState(); })
  .catch(()=>{ $('rnErr').textContent='改名失败（连接不稳）'; });
}
$('rnCancel').onclick=rnClose;
$('rnMask').onclick=e=>{ if(e.target.id==='rnMask')rnClose(); };
$('rnOk').onclick=rnSubmit;
$('rnInput').addEventListener('keydown',e=>{ if(e.key==='Enter')rnSubmit(); if(e.key==='Escape')rnClose(); });