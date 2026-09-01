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
  const txt=add(x.role==='user'?'user':'ai',x.text||'');
  addMsgImgs(txt,x.images);
  return txt;
}
/* ---------- 内联工具卡片（可展开） ---------- */
let lastTool=null,lastToolKey='';
function toolCard(name,args,result){
  const key=name+'|'+JSON.stringify(args||{});
  let card=null;
  if(!result&&lastTool&&lastToolKey===key){card=lastTool;}
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
    toolCard(m.name,m.args,null);
    if(!runs.has(sid)){renderSteps();return;}
    if(m.name==='todo_write'){const ts=(m.args&&m.args.todos)||[];steps=ts.slice(0,20).map((t,i)=>({name:'todo',title:String((t&&t.title)||('步骤 '+(i+1))).slice(0,80),status:(t&&t.status)==='completed'?'done':(t&&t.status)==='in_progress'?'run':'wait'}));planned=true;}
    else if(!planned){const d=m.args||{};const hint=String(d.description||d.command||d.cmd||d.path||d.file||d.pattern||'').slice(0,48);steps.push({name:m.name,title:m.name+(hint?' · '+hint:''),status:'run'});}
    renderSteps();
  }
  else if(m.type==='tool_result'){toolCard(m.name,m.args,m.result!=null?m.result:'');if(!runs.has(sid)){renderSteps();return;}if(m.name!=='todo_write'&&!planned){for(let i=steps.length-1;i>=0;i--){if(steps[i].name===m.name&&steps[i].status==='run'){steps[i].status='done';break;}}}renderSteps();}
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
function applyState(s){
 ALL=s.sessions||[];
 const map={};ALL.forEach(x=>{const k=x.workspace||'';(map[k]=map[k]||{n:0,u:0}).n++;map[k].u=Math.max(map[k].u,x.updated)});
 const w0=s.workspace||'';if(w0&&!map[w0])map[w0]={n:0,u:0};
 const sel=$('proj');
 if(s.workspace&&window.__ws&&s.workspace!==window.__ws)projPin=false;
 const keep=(projPin&&wsCur)?wsCur:(s.workspace||wsCur);
 const opts=Object.keys(map).sort((a,b)=>map[b].u-map[a].u).map(w=>'<option value="'+esc(w)+'">'+esc(w.split('/').filter(Boolean).pop()||w)+' ('+map[w].n+')</option>').join('');
 if(opts!==window.__projOpts){window.__projOpts=opts;sel.innerHTML=opts;}
 wsCur=map[keep]?keep:(sel.options[0]?sel.options[0].value:'');sel.value=wsCur;
 window.__ws=s.workspace||'';window.__branch=s.branch||'';
 runs=new Set(ALL.filter(x=>x.running).map(x=>x.id));
 renderSteps();
 try{$('modeSel').value=s.mode||'always'}catch(e){}
 try{$('ver').textContent=s.version?('v'+s.version):''}catch(e){}
 const _cur=s.current||'';if(_cur&&_cur!==lastCurrent){lastCurrent=_cur;loadModels();}
 const _cs=s.current_session||'';if(_cs&&_cs!==sid&&!cur){openS(_cs,false);}
 // 流式渲染中（cur 存在）不自动跳转/清屏，防止把刚发出的回显和回复洗掉
 if(!sid&&!cur){const cand=ALL.filter(x=>(x.workspace||'')===wsCur).sort((a,b)=>b.updated-a.updated)[0];if(cand)openS(cand.id,false);}
 setSessName();renderSess();badge();
}
function loadState(){req({type:'state'}).then(applyState).catch(()=>{});}
let steps=[],round=0,planned=false;
function renderSteps(){const el=$('steps');if(!el)return;if(!steps.length||!runs.has(sid)){el.classList.remove('show');return;}el.classList.add('show');
 const done=steps.filter(x=>x.status==='done').length;
 el.innerHTML='<div class="stepsbar-head">任务 '+done+'/'+steps.length+' · 运行中'+(round>0?(' · 第 '+round+' 轮'):'')+'</div>'+steps.map(x=>'<div class="step '+x.status+'">'+(x.status==='done'?'✓':x.status==='run'?'●':x.status==='deny'?'✗':'○')+' '+esc(x.title)+'</div>').join('');
 el.scrollTop=el.scrollHeight;}
function renderSess(){
 const rows=ALL.filter(x=>(x.workspace||'')===wsCur).sort((a,b)=>b.updated-a.updated);
 let h='';
 rows.forEach(x=>{h+='<div class="s'+(x.id===sid?' on':'')+'" data-id="'+x.id+'">'
  +(x.running?'<span class="run">▶</span>':'')+'<span class="t">'+esc(x.title||'（无标题）')+'</span>'
  +'<span class="s-act" data-act="rename">✎</span><span class="s-act del" data-act="del">🗑</span>'
  +'<span class="ago">'+ago(x.updated)+'</span></div>';});
 $('sessList').innerHTML=h||'<div class="side-grp" style="padding:8px">该项目暂无会话</div>';
}
function badge(){const c=$('conn');if(c)c.classList.toggle('working',runs.size>0);document.body.classList.toggle('working',runs.size>0);$('run').textContent=runs.size?'▶ '+runs.size:'';$('btnStop').classList.toggle('on',runs.has(sid));renderRuns();}
function mark(id,on){ALL.forEach(x=>{if(x.id===id)x.running=on});renderSess();}
function setSessName(){const el=$('sessName');if(!el)return;const i=ALL.findIndex(x=>x.id===sid);const t=i>=0?ALL[i].title:'';const w=i>=0?((ALL[i].workspace||'').split('/').filter(Boolean).pop()||''):'';
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
 if(!notify){const sx=ALL.find(x=>x.id===id);syncProj(window.__ws||(sx&&sx.workspace)||'');}
 sid=id;closeSide();
 log.innerHTML='';cur=null;renderSess();setSessName();badge();
 if(notify){try{ws.send(JSON.stringify({type:'open_session',id}))}catch(e){}}
 const m=await req({type:'messages',id});
 if(seq!==openSeq)return;
 if(cur)return; // 流式已在渲染（历史拉取期间回复开始），跳过历史重绘防止清屏
 if(m.messages&&m.messages.length)m.messages.forEach(renderMsg);
 else log.innerHTML='<div class="empty">（空会话，直接发第一条消息）</div>';
}
function syncProj(w){w=w||'';if(!w||w===wsCur)return;wsCur=w;const sel=$('proj');for(const o of sel.options){if(o.value===w){sel.value=w;break;}}renderSess();}
$('sessNew').onclick=async()=>{try{const m=await req({type:'new_session',dir:wsCur||window.__ws});if(m&&m.error)add('err',m.error);}catch(e){add('err','新建失败');}loadState();};
$('sessList').onclick=e=>{const act=e.target.closest('.s-act');
 if(act){const row=act.closest('.s');if(!row)return;const id=row.dataset.id;
  if(act.dataset.act==='del'){ if(confirm('删除该会话？')) req({type:'delete_session',id}).then(()=>loadState()); }
  else if(act.dataset.act==='rename'){ const nm=prompt('新标题',''); if(nm&&nm.trim()) req({type:'rename_session',id,title:nm.trim()}).then(()=>loadState()); }
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
$('slash').addEventListener('click',e=>{const si=e.target.closest('.si');if(!si)return;const k=si.dataset.k;
 if(k==='/file'){const i=$('i');i.value='';try{$('fileAny').click()}catch(_){}
 }else{const i=$('i');i.value=k+' ';i.focus();}
 $('slash').classList.remove('open');});
$('i').addEventListener('keydown',e=>{if(e.key==='Escape')$('slash').classList.remove('open');});

/* ---------- 发送 ---------- */
$('f').onsubmit=ev=>{ev.preventDefault();
 const i=$('i');const t=i.value.trim();
 if(t.startsWith('/file')){ i.value='';cur=null;try{$('fileAny').click()}catch(_){}; $('slash').classList.remove('open'); return; }
 if(!t&&!pendingAtts.length)return;
 const mc=t.match(/^\/([a-z0-9_:-]+)(\s+(.*))?$/i);
 if(mc){ i.value='';cur=null;
  wsSend({type:'command',session:sid,command:mc[1],arguments:(mc[3]||'').trim(),atts:pendingAtts});
  pendingAtts=[];try{$('pendingAtts').innerHTML=''}catch(_){}; return; }
 i.value='';cur=null;
 wsSend({type:'send',session:sid,text:t,atts:pendingAtts});
 pendingAtts=[];try{$('pendingAtts').innerHTML=''}catch(_){};
};

/* ---------- 附件（按钮 / 选择 / 粘贴 / 拖放） ---------- */
$('btnAtt').onclick=()=>{try{$('fileAny').click()}catch(_){}};
let pendingAtts=[];
function addAttChip(a){const box=$('pendingAtts');if(!box)return;const c=document.createElement('div');c.className='att-chip';
 if((a.data||'').startsWith('data:image')){const im=document.createElement('img');im.src=a.data;c.appendChild(im);}
 const sp=document.createElement('span');sp.textContent=a.name;c.appendChild(sp);box.appendChild(c);}
function readAsImage(file,cb){const r=new FileReader();r.onload=()=>{const img=new Image();img.onload=()=>{const max=1280;let w=img.width,h=img.height;if(w>max||h>max){if(w>=h){h=Math.round(h*max/w);w=max;}else{w=Math.round(w*max/h);h=max;}}const c=document.createElement('canvas');c.width=w;c.height=h;c.getContext('2d').drawImage(img,0,0,w,h);cb(c.toDataURL('image/jpeg',0.8));};img.src=r.result;};r.readAsDataURL(file);}
function readAsData(file,cb){const r=new FileReader();r.onload=()=>cb(r.result);r.readAsDataURL(file);}
$('fileAny').addEventListener('change',e=>{const f=e.target.files[0];if(!f)return;const isImg=(f.type||'').startsWith('image/');const cb=d=>{pendingAtts.push({name:f.name,data:d});addAttChip({name:f.name,data:d});};if(isImg)readAsImage(f,cb);else readAsData(f,cb);e.target.value='';});
function addPasted(f){if(!f)return;const isImg=(f.type||'').startsWith('image/');const cb=d=>{pendingAtts.push({name:f.name||'pasted',data:d});addAttChip({name:f.name||'pasted',data:d});};if(isImg)readAsImage(f,cb);else readAsData(f,cb);}
$('i').addEventListener('paste',e=>{const items=(e.clipboardData&&e.clipboardData.items)||[];const it=[].slice.call(items).find(x=>x.type&&x.type.startsWith('image/'));if(it){e.preventDefault();addPasted(it.getAsFile());}});
document.addEventListener('dragover',e=>e.preventDefault());
document.addEventListener('drop',e=>{e.preventDefault();const fs=(e.dataTransfer&&e.dataTransfer.files)||[];[].slice.call(fs).forEach(addPasted);});

connect();setInterval(loadState,8000);
