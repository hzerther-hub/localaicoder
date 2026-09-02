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
function setSessName(){const el=$('sessName');if(!el)return;const i=ALL.findIndex(x=>x.id===sid);const t=i>=0?ALL[i].title:'';const w=i>=0?((ALL[i].workspace||'').split('/').filter(Boolean).pop()||''):'';
 el.textContent=t?('会话：'+t+(w?'  ·  '+w:'')):'（选择一个会话）';}
// 运行中会话横幅：跨项目列出所有 running 的会话（标题 · 目录），点击跳转过去看
function renderRuns(){const bar=$('runsBar');if(!bar)return;
 const list=ALL.filter(x=>x.running);
 if(!list.length){bar.classList.remove('on');bar.innerHTML='';return;}
 bar.classList.add('on');
 bar.innerHTML=list.map(x=>'<span class="rr'+(x.id===sid?' cur':'')+'" data-id="'+x.id+'">▶ '+esc((x.title||'（无标题）').slice(0,24))+' · '+esc((x.workspace||'').split('/').filter(Boolean).pop()||'?')+'</span>').join('');}
$('runsBar').onclick=e=>{const c=e.target.closest('.rr');if(!c)return;projPin=false;openS(c.dataset.id,true);};
function req(o){return new Promise(res=>{const id=++rid;pend[id]=res;ws.send(JSON.stringify(Object.assign(o,{rid:id})));});}
function addMsgImgs(container,imgs){if(!imgs||!imgs.forEach||!imgs.length)return;const w=document.createElement('div');w.className='msg-thumbs';imgs.forEach(u=>{const im=document.createElement('img');im.className='msg-img';im.src=u;w.appendChild(im);});container.appendChild(w);log.scrollTop=log.scrollHeight;}
function renderMsg(x){
  const d=add(x.role==='user'?'user':'ai', x.text||'');
  addMsgImgs(d,x.images);
  return d;
}
function add(cls,t){const d=document.createElement('div');d.className=cls;d.textContent=t;log.appendChild(d);log.scrollTop=log.scrollHeight;return d;}
function connect(){
 ws=new WebSocket(WS);
 ws.onmessage=e=>{let m;try{m=JSON.parse(e.data)}catch(_){return}
  if(m.rid&&pend[m.rid]){pend[m.rid](m);delete pend[m.rid];return}
  if(m.type==='permission_request'){permId=m.id;permSid=m.sessionID;const f=m.metadata&&m.metadata.filepath?(' '+m.metadata.filepath):'';const pats=(m.patterns&&m.patterns.length)?(' '+m.patterns.join(' ')):'';$('permTxt').textContent='请求权限 · '+(m.permission||'tool')+f+pats;$('permBar').style.display='flex';return;}
  if(m.sessionId&&sid&&m.sessionId!==sid)return;
  if(m.type==='user_message'){const d=add('user',m.text);addMsgImgs(d,m.images);cur=null;}
  else if(m.type==='text'){if(!cur)cur=add('ai','');cur.textContent+=m.delta;log.scrollTop=log.scrollHeight;}
  else if(m.type==='tool'){add('tool','🔧 '+m.delta);codeAdd('run','▶ '+m.delta);}
 else if(m.type==='error'){add('err',m.delta);cur=null;codeAdd('err',m.delta);}
 else if(m.type==='run:started'){runs.add(m.sessionId);mark(m.sessionId,true);badge();if(m.sessionId===sid){steps=[];round=0;planned=false;renderSteps();codeAdd('meta','● 任务开始');}}
 else if(m.type==='run:finished'){runs.delete(m.sessionId);mark(m.sessionId,false);badge();cur=null;if(m.sessionId===sid){steps=[];renderSteps();codeAdd('meta','● 运行结束');}}
  else if(m.type==='usage'){usage=m.usage||{};total=m.total||{};renderStats();}
 else if(m.type==='round'){round=+m.n;renderSteps();codeAdd('round','── 第 '+m.n+' 轮 ──');$('codeRound').textContent=m.n?('第 '+m.n+' 轮'):'';}
 else if(m.type==='tool_start'){
  codeAdd('run','▶ '+m.name+(codeArgs(m.args)?'   '+codeArgs(m.args):''));
  if(!runs.has(sid)){renderSteps();return;}
  if(m.name==='todo_write'){const ts=(m.args&&m.args.todos)||[];steps=ts.slice(0,20).map((t,i)=>({name:'todo',title:String((t&&t.title)||('步骤 '+(i+1))).slice(0,80),status:(t&&t.status)==='completed'?'done':(t&&t.status)==='in_progress'?'run':'wait'}));planned=true;}
  else if(!planned){steps=steps.filter(x=>x.name!=='…');steps.push({name:m.name,title:m.name,status:'run'});}
  renderSteps();
 }
 else if(m.type==='tool_result'){codeAdd('done','✔ '+m.name+(m.result?'   '+(String(m.result).length>500?String(m.result).slice(0,500)+'…':String(m.result)):''));if(!runs.has(sid)){renderSteps();return;}if(m.name!=='todo_write'&&!planned){for(let i=steps.length-1;i>=0;i--){if(steps[i].name===m.name&&steps[i].status==='run'){steps[i].status='done';break;}}}renderSteps();}
 else if(m.type==='tool_denied'){codeAdd('deny','✗ '+(m.name||'工具'));if(!runs.has(sid)){renderSteps();return;}for(let i=steps.length-1;i>=0;i--){if(steps[i].name===m.name&&steps[i].status==='run'){steps[i].status='deny';break;}}renderSteps();}
 else if(m.type==='model:changed'){loadModels();}
  else if(m.type==='permission:changed'){try{$('modeSel').value=m.value}catch(e){}ddRefresh();}
  else if(m.type==='sessions:changed'){loadState();}
  else if(m.type==='session:opened'){openS(m.id,false);}
  else if(m.type==='fs'){fsSet(!!m.enabled,!!m.safe);}
  else if(m.type==='hello'){fsSet(!!m.fs_enabled,!!m.fs_safe);}
 };
 ws.onopen=()=>{setConn('live');loadState();loadModels();};
 ws.onclose=(e)=>{let why='';const c=e&&e.code;if(c&&c!==1000)why=' (code '+c+')';setConn('dead');add('err','连接断开'+why+'，10 秒后重连…');setTimeout(()=>{log.innerHTML='';connect();},10000);};
}
function applyState(s){
 fsSet(!!s.fs,!!s.fs_safe);
 ALL=s.sessions||[];
 const map={};ALL.forEach(x=>{const k=x.workspace||'';(map[k]=map[k]||{n:0,u:0}).n++;map[k].u=Math.max(map[k].u,x.updated)});
 // 保证当前项目总是出现在下拉（即使还没会话，新建项目也能看到）
 const w0=s.workspace||'';if(w0&&!map[w0])map[w0]={n:0,u:0};
 const sel=$('proj');
 // PC 端切换项目（s.workspace 变化）→ 清除手机端手动锁定；未锁定时顶部镜像实际工作区
 if(s.workspace&&window.__ws&&s.workspace!==window.__ws)projPin=false;
 const keep=(projPin&&wsCur)?wsCur:(s.workspace||wsCur);
 const opts=Object.keys(map).sort((a,b)=>map[b].u-map[a].u).map(w=>'<option value="'+esc(w)+'">'+esc(w.split('/').filter(Boolean).pop()||w)+' ('+map[w].n+')</option>').join('');
 if(!opts)opts='<option value="">等待 PC 连接…</option>'; // 空状态占位，避免页头出现空白框
 if(opts!==window.__projOpts){window.__projOpts=opts;sel.innerHTML=opts;} // 选项没变不重建，避免打断手机端展开中的下拉
 wsCur=map[keep]?keep:(sel.options[0]?sel.options[0].value:'');sel.value=wsCur;
 window.__ws=s.workspace||'';window.__branch=s.branch||'';try{compact=(s.compact&&{budget:Number(s.compact.budget)||0,window:Number(s.compact.window)||0})||compact}catch(e){}
renderStats();
runs=new Set(ALL.filter(x=>x.running).map(x=>x.id));
  renderSteps(); // 状态刷新后重算步骤栏（含 runs 更新后，保证当前会话运行中能显示）
 fsSyncAvail();
 try{$('modeSel').value=s.mode||'always'}catch(e){}
 const _cur=s.current||'';if(_cur&&_cur!==lastCurrent){lastCurrent=_cur;loadModels();}
 const _cs=s.current_session||'';if(_cs&&_cs!==sid){openS(_cs,false);}
 setSessName();renderSess();badge();ddRefresh();
}
function loadState(){req({type:'state'}).then(applyState);}
let steps=[],round=0,planned=false;
let usage={},total={},compact={budget:0,window:0};
function fmtN(n){n=Number(n)||0;return n>=1e6?(n/1e6).toFixed(2)+'M':n>=1e3?(n/1e3).toFixed(1)+'k':String(n)}
function renderStats(){const el=$('stats');if(!el)return;const u=usage,t=total,c=compact;
 const turnHit=u.prompt_tokens>0?((u.cached_tokens||0)/u.prompt_tokens*100).toFixed(1)+'%':'—';
 const avgHit=t.prompt_tokens>0?((t.cached_tokens||0)/t.prompt_tokens*100).toFixed(2)+'%':'—';
 const ctx=c.window>0?Math.min(100,(u.prompt_tokens||0)/c.window*100).toFixed(0)+'%':'—';
 const comp=c.window>0?Math.min(100,(c.budget/c.window)*100).toFixed(0)+'%':'—';
 const ws=(window.__ws||'').split('/').filter(Boolean).slice(-2).join('/');
 // 关键指标排前：折叠成一行时先露「本次命中 / 会话 / 本次缓存 / 本次费用」
 const parts=['⌂ '+ws,'⎇ '+(window.__branch||'—'),'本次命中 '+turnHit,'会话 '+fmtN(t.total_tokens||0),'本次缓存 '+fmtN(u.cached_tokens||0),'平均命中 '+avgHit,'本次 tokens '+fmtN(u.total_tokens||0),'输出 '+fmtN(u.completion_tokens||0),'本次费用 '+((u.cost_usd&&u.cost_usd>0)?('¥'+(u.cost_usd*7.2).toFixed(4)):'—'),'上下文 '+ctx,'压缩阈值 '+comp];
 el.innerHTML=parts.map(function(x){return '<span class="st">'+esc(x)+'</span>'}).join('');el.classList.add('show');const _w=el.parentElement;if(_w&&_w.classList)_w.classList.add('on');}
function renderSteps(){const el=$('steps');if(!el)return;if(!steps.length||!runs.has(sid)){el.classList.remove('show');return;}el.classList.add('show');
 const done=steps.filter(x=>x.status==='done').length;
 const head=done+'/'+steps.length+' · 运行中 · 第 '+round+' 轮';
 el.innerHTML='<div class="stepsbar-head">任务步骤 '+esc(head)+'</div>'+steps.map(x=>'<div class="step '+x.status+'">'+(x.status==='done'?'✅':x.status==='run'?'●':x.status==='deny'?'✗':'○')+' '+esc(x.title)+'</div>').join('');
 el.scrollTop=el.scrollHeight;}
function renderSess(){
 const rows=ALL.filter(x=>(x.workspace||'')===wsCur).sort((a,b)=>b.updated-a.updated);
 let h='<div class="gr">项目会话（'+rows.length+'）</div>';
 rows.forEach(x=>{h+='<div class="s'+(x.id===sid?' on':'')+'" data-id="'+x.id+'">'
  +(x.running?'<span class="run">▶</span>':'')+'<span class="t">'+esc(x.title)+'</span>'
  +'<span class="s-act" data-act="rename">✎</span><span class="s-act del" data-act="del">🗑</span>'
  +'<span class="ago">'+ago(x.updated)+'</span></div>';});
 $('sessList').innerHTML=h||'<div class="gr">该项目暂无会话</div>';
}
function badge(){const c=$('conn');if(c)c.classList.toggle('working',runs.size>0);document.body.classList.toggle('working',runs.size>0);$('run').textContent=runs.size?'▶ '+runs.size:'';renderRuns();}
function mark(id,on){ALL.forEach(x=>{if(x.id===id)x.running=on});renderSess();}
// 顶部项目下拉同步到指定工作区
function syncProj(w){w=w||'';if(!w||w===wsCur)return;wsCur=w;const sel=$('proj');for(const o of sel.options){if(o.value===w){sel.value=w;break;}}renderSess();}
async function openS(id,notify){
 const seq=++openSeq;
 // PC 端打开的会话：顶部同步实际工作区（桥单工作区下会话存的 directory 只代表创建地）；
 // 手机端自己点的会话不动当前项目选择
 if(!notify){const sx=ALL.find(x=>x.id===id);syncProj(window.__ws||(sx&&sx.workspace)||'');}
 sid=id;$('drawer').classList.remove('open');log.innerHTML='';cur=null;renderSess();fsSyncAvail();codeLog.innerHTML='';codeSticky=true;$('codeRound').textContent='';
 setSessName();
 if(notify){try{ws.send(JSON.stringify({type:'open_session',id}))}catch(e){}}
 const m=await req({type:'messages',id});
 if(seq!==openSeq)return; // 已有更新的 openS，丢弃本次渲染（防切换并发重复）
 if(m.messages&&m.messages.length)m.messages.forEach(renderMsg);
 else log.innerHTML='<div class="empty">（空会话，直接发第一条消息）</div>';
}
async function loadModels(){
 let d; try{ d=await req({type:'models'});}catch(e){d=null;}
  const sel=$('model');
 if(!d||!d.models||!d.models.length){ sel.innerHTML='<option value="">正在连接桌面…</option>'; fillEffort(null); return; }
 sel.innerHTML=d.models.map(x=>'<option value="'+esc(x.key)+'"'+(x.is_current?' selected':'')+'>'+esc(x.model_id||x.display_name)+(x.vision?' 👁':'')+(x.reasoning?' 🧠':'')+'</option>').join('');
 fillEffort(d.models.find(x=>x.is_current)||null);
}
function fillEffort(mi){
  const ef=$('effort');
 const choices=(mi&&mi.reasoning&&mi.reasoning_choices)?mi.reasoning_choices:[];
 ef.innerHTML=choices.length
   ? choices.map(c=>'<option value="'+esc(c)+'"'+((mi.reasoning_effort||'')===c?' selected':'')+'>'+esc(c===''?'默认':c)+'</option>').join('')
   : '<option value="">—</option>';
 if(mi&&mi.reasoning_effort)ef.value=mi.reasoning_effort;
 ddRefresh();
}
$('model').onchange=async()=>{const k=$('model').value;try{await req({type:'model',key:k});}catch(e){}
 try{const d=await req({type:'models'});if(d)sel2models(d);}catch(e){}
 function sel2models(d){const s=$('model');s.innerHTML=d.models.map(x=>'<option value="'+esc(x.key)+'"'+(x.is_current?' selected':'')+'>'+esc(x.model_id||x.display_name)+'</option>').join('');fillEffort(d.models.find(x=>x.is_current)||null);}
};
$('effort').onchange=async()=>{const k=$('model').value,e=$('effort').value;try{await req({type:'effort',key:k,effort:e});}catch(e){}};
$('modeSel').onchange=async()=>{const v=$('modeSel').value;try{await req({type:'mode',value:v});}catch(e){}};
$('statsToggle').onclick=()=>{const w=$('stats').parentElement;if(!w)return;w.classList.toggle('expanded');$('statsToggle').textContent=w.classList.contains('expanded')?'▾':'▸';};
$('sessList').onclick=e=>{const act=e.target.closest('.s-act');
 if(act){const row=act.closest('.s');if(!row)return;const id=row.dataset.id;
  if(act.dataset.act==='del'){ if(confirm('删除该会话？')) req({type:'delete_session',id}).then(()=>loadState()); }
  else if(act.dataset.act==='rename'){ const sx=ALL.find(x=>x.id===id); rnOpen(id,(sx&&sx.title)||''); }
  return; }
 const r=e.target.closest('.s');if(r)openS(r.dataset.id,true);};
$('btnS').onclick=()=>{$('drawer').classList.toggle('open');if($('drawer').classList.contains('open')){loadState();reqDir(window.__ws||wsCur||'/');}};
// ---- 工作目录浏览：列子目录→下钻→在该目录新建会话（桥: dir_list / new_session） ----
let dirCur='';
function renderDir(m){
 if(!m||m.type!=='dir_list')return;
 dirCur=m.path||dirCur;
 $('dirNow').textContent='📂 '+dirCur;$('dirInput').value=dirCur;
 const up=dirCur.replace(/\/+$/,'').replace(/\/[^/]*$/,'')||'/';
 let h='<div class="dir-item up" data-p="'+esc(up)+'">↑ 上级</div>';
 h+=(m.dirs||[]).map(d=>'<div class="dir-item" data-p="'+esc(d.path)+'">'+esc(d.name)+'</div>').join('');
 if(m.error)h+='<div class="gr">'+esc(m.error)+'</div>';
 else if(!(m.dirs||[]).length)h+='<div class="gr">（无子目录）</div>';
 $('dirList').innerHTML=h;
}
function reqDir(p){p=(p||'').trim();if(!p)return;req({type:'dir_list',path:p}).then(renderDir).catch(()=>{});}
$('dirGo').onclick=()=>{const v=$('dirInput').value.trim();if(v)reqDir(v);};
$('dirInput').addEventListener('keydown',e=>{if(e.key==='Enter'){e.preventDefault();$('dirGo').click();}});
$('dirList').onclick=e=>{const it=e.target.closest('.dir-item');if(!it)return;reqDir(it.dataset.p);};

// ---- WEB 文件浏览 / 编辑（电脑端 fs_enabled + fs_safe 开关门控） ----
let fsOn=false,fsSafeMode=true,fsDir='',fsViewPath='',fsViewRaw='',fsEditing=false,fsEdittable=false;
const fsMaxEdit=200*1024;
function fsSet(on,safe){ fsOn=on; if(safe!=null)fsSafeMode=safe;
 const e=$('btnFiles'); if(e)e.style.display=on?'':'none';
 const t=$('fsPanelTitle'); if(t)t.textContent=on?('📁 文件'+(fsSafeMode?' 🔒 安全目录（仅当前项目）':' ⚠ 任意路径')):'📁 文件';
 if(!on){$('fsPanel').classList.remove('on');$('fsView').classList.remove('on');} }
function fsHuman(n){ if(n>=1048576)return (n/1048576).toFixed(1)+'M'; if(n>=1024)return (n/1024).toFixed(1)+'K'; return n+'B'; }
const fsExtColor={js:'#e5c07b',mjs:'#e5c07b',ts:'#3178c6',tsx:'#3178c6',jsx:'#e5c07b',py:'#3572A5',go:'#00ADD8',rs:'#dea584',
 html:'#e34c26',htm:'#e34c26',vue:'#41b883',css:'#563d7c',scss:'#c6538c',json:'#98c379',md:'#98c379',yml:'#cb171e',yaml:'#cb171e',
 sh:'#89e051',bat:'#c1f12e',ps1:'#4f99e0',psm1:'#4f99e0',sql:'#e38c00',java:'#b07219',c:'#555555',h:'#555555',cpp:'#f34b7d',cs:'#178600',rb:'#701516',php:'#4F5D95',swift:'#F05138'};
function fsColor(name){ const m=(name||'').split('.').pop().toLowerCase(); return fsExtColor[m]||'#8a93a5'; }
function fsSend(o){ if(ws&&ws.readyState===1){ws.send(JSON.stringify(o));return true;} fsToast('连接未就绪'); return false; }
function fsOpenPanel(){ if(!fsOn){ fsToast('未开启 WEB 文件访问：电脑端「移动端远程控制」面板打开开关'); return; }
 if(!sid){ fsToast('先打开一个会话，文件列表随会话所属项目打开'); return; }
 $('fsPanel').classList.add('on'); fsNav(window.__ws||''); }
function fsSyncAvail(){ // 未选择会话时置灰文件入口（面板操作归属于当前会话的项目）
 const d=!sid; ['hdrFiles','btnFiles'].forEach(id=>{ const b=$(id); if(b){ b.disabled=d; b.style.opacity=d?'.45':''; } });
 const e=$('btnFiles'); if(e)e.style.display=fsOn?'':'none'; }
$('btnFiles').onclick=fsOpenPanel;
if($('hdrFiles'))$('hdrFiles').onclick=fsOpenPanel;
$('fsClose').onclick=()=>$('fsPanel').classList.remove('on');
$('fsGo').onclick=()=>fsNav($('fsPath').value.trim());
$('fsPath').addEventListener('keydown',e=>{ if(e.key==='Enter')fsNav($('fsPath').value.trim()); });
function fsCrumb(p){
 const el=$('fsCrumb'); if(!p){el.innerHTML='<span class="fs-c on">此电脑（磁盘列表）</span>';return;}
 const segs=String(p).split(/[\\/]/).filter(Boolean);
 let acc='',h='<span class="fs-c" data-p="">此电脑</span>';
 segs.forEach((s,i)=>{ if(i===0&&/^[A-Za-z]:$/.test(s)){acc=s+'\\';h+=' <span class="fs-c on">'+esc(s)+'\\</span>';return;}
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
 }catch(e){ fsToast('读取失败'); }
}
function renderFsList(m){
 const el=$('fsList'); let h='';
 if(fsDir){ const parent=fsDir.replace(/[\/]+$/,'').replace(/[\/][^\/]*$/,'')||'';
  h+='<div class="fs-row dir" data-p="'+esc(parent)+'"><span class="fs-ic">↩︎</span><span class="fs-nm">..</span></div>'; }
 (m.dirs||[]).forEach(d=>{ h+='<div class="fs-row dir" data-p="'+esc(d.path)+'"><span class="fs-ic">📁</span><span class="fs-nm">'+esc(d.name)+'</span><span class="fs-more" data-more="1">⋯</span></div>'; });
 (m.files||[]).forEach(f=>{ h+='<div class="fs-row file" data-p="'+esc(f.path)+'" data-sz="'+(f.size||0)+'"><span class="fs-ic" style="color:'+fsColor(f.name)+'">●</span><span class="fs-nm" style="color:'+fsColor(f.name)+'">'+esc(f.name)+'</span><span class="fs-sz">'+fsHuman(f.size||0)+'</span><span class="fs-more" data-more="1">⋯</span></div>'; });
 el.innerHTML=h||'<div class="gr">（空目录）</div>';
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
 catch(e){ fsToast('切换失败'); }
};
async function fsOpenFile(path,size){
 let m; try{ m=await req({type:'fs_read',path}); }catch(e){ fsToast('读取超时'); return; }
 if(m.error){ fsToast(m.error); return; }
 fsViewPath=m.path||path; fsViewRaw=m.content||'';
 fsEdittable=!m.truncated&&(m.size||size||0)<=fsMaxEdit;
 $('fvName').textContent=m.name||path.split(/[\/]/).pop();
 $('fvMeta').textContent=(m.size?fsHuman(m.size):'')+(m.truncated?' · 已截断（>1MB 只读）':'')+(fsEdittable?'':' · 大文件只读');
 fsEditing=false; fsRenderView(); $('fsView').classList.add('on');
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
 $('fvEdit').style.display=(fsEdittable&&!fsEditing)?'':'none';
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
 }catch(e){ fsToast('保存失败'); }
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
$('fvDesktop').onclick=()=>{ if(fsSend({type:'fs_open_desktop',path:fsViewPath}))fsToast('已在电脑端打开'); };
function fsMinimize(){ $('fsView').classList.remove('on'); $('fsMinBar').classList.add('on'); }
function fsRestore(){ $('fsMinBar').classList.remove('on'); $('fsView').classList.add('on');
 if(fsEditing){ fsUpdateSelBtn(); const ta=$('fvTa'); if(ta)ta.focus(); } }
$('fsMinBar').onclick=fsRestore;
$('fvMin').onclick=()=>{ $('fsMinName').textContent=$('fvName').textContent; fsMinimize(); };
$('fvClose').onclick=()=>{ if(fsEditing&&!confirm('正在编辑中，关闭将丢弃未保存的修改？'))return;
 fsEditing=false; $('fsMinBar').classList.remove('on'); $('fsView').classList.remove('on'); };
let fsToastTimer=null;
function fsToast(t){ const el=$('fsHint'); if(!el){add('err',t);return;} el.textContent=t; el.style.color='#e5484d';
 clearTimeout(fsToastTimer); fsToastTimer=setTimeout(()=>{el.textContent='';},4000); }
$('dirNew').onclick=async()=>{
 const p=($('dirInput').value.trim()||dirCur);if(!p)return;
 try{const m=await req({type:'new_session',dir:p});if(m&&m.ok===false&&m.error)add('err',m.error);}
 catch(e){add('err','新建失败');}
};
$('permBar').onclick=e=>{const b=e.target.closest('button');if(!b||!permId)return;const v=b.dataset.p;try{ws.send(JSON.stringify({type:'permission_response',id:permId,response:v,sessionID:permSid}));}catch(_){}permId=null;$('permBar').style.display='none';};
$('drawer').onclick=e=>{if(e.target.id==='drawer')$('drawer').classList.remove('open')};
// ---- 美化下拉：原生 select 隐藏为数据源，套自定义按钮+弹层（onchange 逻辑不变） ----
function ddClose(except){document.querySelectorAll('.dd-pop.open').forEach(p=>{if(p!==except)p.classList.remove('open')});document.body.classList.remove('dd-open');if(document.querySelector('.dd-pop.open'))document.body.classList.add('dd-open');}
function ddLabel(sel){const w=sel.closest('.ddw');if(!w)return;const o=sel.selectedOptions&&sel.selectedOptions[0];w.querySelector('.dd-btn').textContent=o?o.textContent:'';}
function ddBuild(sel){const pop=sel.closest('.ddw').querySelector('.dd-pop');
 pop.innerHTML=[...sel.options].map(o=>'<div class="dd-i'+(o.selected?' on':'')+'" data-v="'+esc(o.value)+'">'+esc(o.textContent)+'</div>').join('');}
function ddRefresh(){document.querySelectorAll('select.dd-native').forEach(s=>ddLabel(s));}
function beautifySelect(sel){if(!sel||sel.dataset.dd)return;sel.dataset.dd='1';sel.classList.add('dd-native');
 const w=document.createElement('div');w.className='ddw';
 const btn=document.createElement('button');btn.type='button';btn.className='dd-btn';
 const pop=document.createElement('div');pop.className='dd-pop';
 sel.parentNode.insertBefore(w,sel);w.append(btn,pop,sel);
 btn.onclick=e=>{e.stopPropagation();const was=pop.classList.contains('open');ddClose(null);if(!was){ddBuild(sel);pop.classList.add('open');}};
 pop.classList.contains('open')&&document.body.classList.add('dd-open');
 pop.onclick=e=>{const o=e.target.closest('[data-v]');if(!o)return;sel.value=o.dataset.v;ddLabel(sel);pop.classList.remove('open');sel.dispatchEvent(new Event('change'));};
 ddLabel(sel);}
document.addEventListener('click',()=>ddClose(null));
['proj','model','effort','modeSel','syncSel'].forEach(id=>beautifySelect($(id)));
// ---- 编程过程面板（聊天 ⇋ 过程 切换） ----
const codeLog=$('codeLog');let codeSticky=true;
codeLog.addEventListener('scroll',()=>{codeSticky=codeLog.scrollTop+codeLog.clientHeight>=codeLog.scrollHeight-48;});
$('codeBtn').onclick=()=>{const on=document.body.classList.toggle('coding');$('codeBtn').textContent=on?'💬 返回聊天':'⌨ 编程过程';if(on)codeLog.scrollTop=codeLog.scrollHeight;};
$('codeClear').onclick=()=>{codeLog.innerHTML='';};
function codeAdd(cls,t){if(t===''||t==null)return;const d=document.createElement('div');d.className='cl '+cls;d.textContent=t;codeLog.appendChild(d);
 while(codeLog.childNodes.length>400)codeLog.removeChild(codeLog.firstChild);
 if(codeSticky)codeLog.scrollTop=codeLog.scrollHeight;}
function codeArgs(a){if(!a||typeof a!=='object')return '';
 const K=['path','file','cmd','command','pattern','query','glob','url','dir'];const p=[];
 for(const k of K){if(a[k]!=null&&a[k]!=='')p.push(k+'='+String(a[k]).slice(0,120));}
 if(!p.length){try{const s=JSON.stringify(a);if(s&&s!=='{}')p.push(s.slice(0,120));}catch(e){}}
 return p.join('   ');}
// 项目同步方向（存本机 localStorage）：pc2m=跟随 PC（PC→手机）；m2pc=驱动 PC（手机→PC）
const syncDir=()=>{try{return localStorage.getItem('projSync')||'pc2m'}catch(e){return 'pc2m'}};
try{$('syncSel').value=syncDir()}catch(e){}
$('syncSel').onchange=()=>{const v=$('syncSel').value;try{localStorage.setItem('projSync',v)}catch(e){}};
$('proj').onchange=()=>{wsCur=$('proj').value;renderSess();
 if(syncDir()==='m2pc'){ // 手机→PC：让电脑切到该项目（状态回包后自动对齐；不支持的后端回错误帧则回退本地浏览）
  try{req({type:'workspace',dir:wsCur}).then(m=>{if(m&&m.type==='error'){add('err',m.delta||'切换失败');projPin=true;}}).catch(()=>{projPin=true;});}catch(e){projPin=true;}
  return;}
 projPin=true; // PC→手机：仅本地浏览锁定，PC 端切项目时自动跟随
};
$('qc').onclick=e=>{const b=e.target.closest('[data-p]');if(!b)return;const i=$('i');i.value=(i.value?i.value+'\n':'')+b.dataset.p;i.focus();};
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
// 粘贴（图片/文件）
$('i').addEventListener('paste',e=>{const items=(e.clipboardData&&e.clipboardData.items)||[];const it=[].slice.call(items).find(x=>x.type&&x.type.startsWith('image/'));if(it){e.preventDefault();addPasted(it.getAsFile());}});
// 拖放（图片/文件）→ 加入附件
document.addEventListener('dragover',e=>e.preventDefault());
document.addEventListener('drop',e=>{e.preventDefault();const fs=(e.dataTransfer&&e.dataTransfer.files)||[];[].slice.call(fs).forEach(addPasted);});
// /init 快捷命令：与电脑端 Composer 的 /init 同一份提示词（分析代码库 → 生成/修订 AGENTS.md）
const INIT_PROMPT='请分析当前工作目录的代码库，并在工作区根目录生成/更新 AGENTS.md（供后续 AI 助手快速了解本项目）。要求：1) 先浏览目录结构与关键入口（README、构建脚本、依赖清单、配置文件）再动笔；2) 内容包含：项目一句话概述、技术栈与关键依赖、目录结构导览、构建/测试/运行命令、代码约定（命名/注释/错误处理）、注意事项；3) 用中文书写，信息必须来自真实文件内容，不要编造；若已存在 AGENTS.md，按现状修订而非推倒重写。';
const CMDS=[['/init','生成/修订 AGENTS.md，让 AI 快速了解本项目'],['/file','选择文件/图片，传给PC识别'],['/列目录','列出当前项目的目录结构'],['/找bug','分析这个项目，找出可能的 BUG 并给出修复'],['/写测试','为最近的代码改动补充单元测试'],['/总结','总结最近的 git 提交和改动要点'],['/重构','分析当前代码，给出重构建议'],['/解释','解释当前项目的作用和整体结构']];
function slashShow(q){
 const box=$('slash');
 const list=CMDS.filter(c=>!q||c[0].slice(1).toLowerCase().includes(q.toLowerCase())).slice(0,8);
 if(!list.length){box.classList.remove('open');return;}
 box.innerHTML=list.map(c=>'<div class="si" data-k="'+esc(c[0])+'" data-t="'+esc(c[1])+'"><span class="k">'+c[0]+'</span><span class="d">'+esc(c[1])+'</span></div>').join('');
 box.classList.add('open');
}
$('i').addEventListener('input',()=>{const v=$('i').value;if(v.startsWith('/'))slashShow(v.slice(1));else $('slash').classList.remove('open');});
$('slash').addEventListener('click',e=>{const si=e.target.closest('.si');if(!si)return;const k=si.dataset.k;
 if(k==='/init'){const i=$('i');i.value=INIT_PROMPT;i.focus();}
 else if(k==='/file'){const i=$('i');i.value='';try{$('fileAny').click()}catch(_){}
 }else{const i=$('i');i.value=si.dataset.t;i.focus();}
 $('slash').classList.remove('open');});
$('i').addEventListener('keydown',e=>{if(e.key==='Escape')$('slash').classList.remove('open');});
$('f').onsubmit=ev=>{ev.preventDefault();
 const i=$('i');let t=i.value.trim();
 // 手动敲 /init 回车：展开为与电脑端一致的完整提示词再发送
 if(t==='/init')t=INIT_PROMPT;
 // /file 不当聊天消息发：激活上传（拉选择器，清空输入框）
 if(t.startsWith('/file')){ i.value='';cur=null;try{$('fileAny').click()}catch(_){}; $('slash').classList.remove('open'); return; }
 if(!t&&!pendingAtts.length&&!fsPendRefs.length)return;
 if(!sid)return;
 i.value='';cur=null;
 ws.send(JSON.stringify({type:'send',session:sid,text:t,atts:pendingAtts,refs:(fsPendRefs.length?fsPendRefs:null)}));
 pendingAtts=[];fsPendRefs=[];try{$('pendingAtts').innerHTML=''}catch(_){};
};
connect();setInterval(loadState,8000);

// ---- 重命名会话弹窗：预填当前标题，校验空名/同项目重名 ----
let rnId='';
function rnOpen(id,cur){ rnId=id; $('rnInput').value=cur; $('rnErr').textContent='';
 $('rnMask').classList.add('on'); setTimeout(()=>{ $('rnInput').focus(); $('rnInput').select(); },60); }
function rnClose(){ $('rnMask').classList.remove('on'); }
function rnSubmit(){
 const v=$('rnInput').value.trim();
 if(!v){ $('rnErr').textContent='名称不能为空'; return; }
 const sx=ALL.find(x=>x.id===rnId); const ws=(sx&&sx.workspace)||'';
 if(ALL.some(x=>x.id!==rnId&&(x.workspace||'')===ws&&(x.title||'').trim()===v)){
  $('rnErr').textContent='该项目下已有同名会话'; return; }
 req({type:'rename_session',id:rnId,title:v}).then(()=>{ rnClose(); loadState(); })
  .catch(()=>{ $('rnErr').textContent='改名失败（连接不稳）'; });
}
$('rnCancel').onclick=rnClose;
$('rnMask').onclick=e=>{ if(e.target.id==='rnMask')rnClose(); };
$('rnOk').onclick=rnSubmit;
$('rnInput').addEventListener('keydown',e=>{ if(e.key==='Enter')rnSubmit(); if(e.key==='Escape')rnClose(); });
$('ddMask').onclick=()=>ddClose(null);
