const RELAY=true;
const D=new URLSearchParams(location.search).get('d');
const WS=(location.protocol==='https:'?'wss://':'ws://')+location.host+'/s/ws?d='+D;
const $=id=>document.getElementById(id);
const log=$('log');
let ALL=[],wsCur='',sid='',runs=new Set(),cur=null,ws,rid=0,lastCurrent='',openSeq=0,permId=null,permSid='';
const pend={};
const esc=s=>{const d=document.createElement('div');d.textContent=s;return d.innerHTML;};
const ago=ts=>{const d=Date.now()/1000-ts;if(d<60)return '刚刚';if(d<3600)return Math.floor(d/60)+'分';if(d<86400)return Math.floor(d/3600)+'时';return Math.floor(d/86400)+'天';};
function setConn(s){const el=$('conn');if(el)el.className=s;}
function setSessName(){const el=$('sessName');if(!el)return;const i=ALL.findIndex(x=>x.id===sid);const t=i>=0?ALL[i].title:'';el.textContent=t?('会话：'+t):'（选择一个会话）';}
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
  else if(m.type==='tool'){add('tool','🔧 '+m.delta);}
  else if(m.type==='error'){add('err',m.delta);cur=null;}
  else if(m.type==='run:started'){runs.add(m.sessionId);mark(m.sessionId,true);badge();if(m.sessionId===sid){steps=[];round=0;planned=false;renderSteps();}}
  else if(m.type==='run:finished'){runs.delete(m.sessionId);mark(m.sessionId,false);badge();cur=null;if(m.sessionId===sid){steps=[];renderSteps();}}
  else if(m.type==='usage'){usage=m.usage||{};total=m.total||{};renderStats();}
 else if(m.type==='round'){round=+m.n;renderSteps();}
 else if(m.type==='tool_start'){
  if(!runs.has(sid)){renderSteps();return;}
  if(m.name==='todo_write'){const ts=(m.args&&m.args.todos)||[];steps=ts.slice(0,20).map((t,i)=>({name:'todo',title:String((t&&t.title)||('步骤 '+(i+1))).slice(0,80),status:(t&&t.status)==='completed'?'done':(t&&t.status)==='in_progress'?'run':'wait'}));planned=true;}
  else if(!planned){steps=steps.filter(x=>x.name!=='…');steps.push({name:m.name,title:m.name,status:'run'});}
  renderSteps();
 }
 else if(m.type==='tool_result'){if(!runs.has(sid)){renderSteps();return;}if(m.name!=='todo_write'&&!planned){for(let i=steps.length-1;i>=0;i--){if(steps[i].name===m.name&&steps[i].status==='run'){steps[i].status='done';break;}}}renderSteps();}
 else if(m.type==='tool_denied'){if(!runs.has(sid)){renderSteps();return;}for(let i=steps.length-1;i>=0;i--){if(steps[i].name===m.name&&steps[i].status==='run'){steps[i].status='deny';break;}}renderSteps();}
 else if(m.type==='model:changed'){loadModels();}
  else if(m.type==='permission:changed'){try{$('modeSel').value=m.value}catch(e){}}
  else if(m.type==='sessions:changed'){loadState();}
  else if(m.type==='session:opened'){openS(m.id,false);}
 };
 ws.onopen=()=>{setConn('live');loadState();loadModels();};
 ws.onclose=(e)=>{let why='';const c=e&&e.code;if(c&&c!==1000)why=' (code '+c+')';setConn('dead');add('err','连接断开'+why+'，10 秒后重连…');setTimeout(()=>{log.innerHTML='';connect();},10000);};
}
function applyState(s){
 ALL=s.sessions||[];
 const map={};ALL.forEach(x=>{const k=x.workspace||'';(map[k]=map[k]||{n:0,u:0}).n++;map[k].u=Math.max(map[k].u,x.updated)});
 // 保证当前项目总是出现在下拉（即使还没会话，新建项目也能看到）
 const w0=s.workspace||'';if(w0&&!map[w0])map[w0]={n:0,u:0};
 const sel=$('proj');const keep=s.workspace||wsCur;
 sel.innerHTML=Object.keys(map).sort((a,b)=>map[b].u-map[a].u).map(w=>'<option value="'+esc(w)+'">'+esc(w.split('/').filter(Boolean).pop()||w)+' ('+map[w].n+')</option>').join('');
 // 顶部项目始终镜像实际工作区（与底部状态栏同源）：桥是单工作区，
 // 会话可跨项目续用，会话里存的 directory 只代表创建地、会过期
 wsCur=map[keep]?keep:(sel.options[0]?sel.options[0].value:'');sel.value=wsCur;
 window.__ws=s.workspace||'';window.__branch=s.branch||'';try{compact=(s.compact&&{budget:Number(s.compact.budget)||0,window:Number(s.compact.window)||0})||compact}catch(e){}
renderStats();
runs=new Set(ALL.filter(x=>x.running).map(x=>x.id));
  renderSteps(); // 状态刷新后重算步骤栏（含 runs 更新后，保证当前会话运行中能显示）
 try{$('modeSel').value=s.mode||'always'}catch(e){}
 const _cur=s.current||'';if(_cur&&_cur!==lastCurrent){lastCurrent=_cur;loadModels();}
 const _cs=s.current_session||'';if(_cs&&_cs!==sid){openS(_cs,false);}
 setSessName();renderSess();badge();
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
function badge(){const c=$('conn');if(c)c.classList.toggle('working',runs.size>0);$('run').textContent=runs.size?'▶ '+runs.size:'';}
function mark(id,on){ALL.forEach(x=>{if(x.id===id)x.running=on});renderSess();}
// 顶部项目下拉同步到指定工作区
function syncProj(w){w=w||'';if(!w||w===wsCur)return;wsCur=w;const sel=$('proj');for(const o of sel.options){if(o.value===w){sel.value=w;break;}}renderSess();}
async function openS(id,notify){
 const seq=++openSeq;
 // 会话统一在实际工作区运行：顶部以实际工作区为准（会话存的 directory 只代表创建地）
 const sx=ALL.find(x=>x.id===id);syncProj(window.__ws||(sx&&sx.workspace)||'');
 sid=id;$('drawer').classList.remove('open');log.innerHTML='';cur=null;renderSess();
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
  else if(act.dataset.act==='rename'){ const nm=prompt('新标题',''); if(nm&&nm.trim()) req({type:'rename_session',id,title:nm.trim()}).then(()=>loadState()); }
  return; }
 const r=e.target.closest('.s');if(r)openS(r.dataset.id,true);};
$('btnS').onclick=()=>{$('drawer').classList.toggle('open');if($('drawer').classList.contains('open'))loadState();};
$('permBar').onclick=e=>{const b=e.target.closest('button');if(!b||!permId)return;const v=b.dataset.p;try{ws.send(JSON.stringify({type:'permission_response',id:permId,response:v,sessionID:permSid}));}catch(_){}permId=null;$('permBar').style.display='none';};
$('drawer').onclick=e=>{if(e.target.id==='drawer')$('drawer').classList.remove('open')};
$('proj').onchange=()=>{wsCur=$('proj').value;renderSess();};
$('qc').onclick=e=>{const b=e.target.closest('[data-p]');if(!b)return;const i=$('i');i.value=(i.value?i.value+'\n':'')+b.dataset.p;i.focus();};
let pendingAtts=[];
function addAttChip(a){const box=$('pendingAtts');if(!box)return;const c=document.createElement('div');c.className='att-chip';
 if((a.data||'').startsWith('data:image')){const im=document.createElement('img');im.src=a.data;c.appendChild(im);}
 const sp=document.createElement('span');sp.textContent=a.name;c.appendChild(sp);box.appendChild(c);}
function readAsImage(file,cb){const r=new FileReader();r.onload=()=>{const img=new Image();img.onload=()=>{const max=1280;let w=img.width,h=img.height;if(w>max||h>max){if(w>=h){h=Math.round(h*max/w);w=max;}else{w=Math.round(w*max/h);h=max;}}const c=document.createElement('canvas');c.width=w;c.height=h;c.getContext('2d').drawImage(img,0,0,w,h);cb(c.toDataURL('image/jpeg',0.8));};img.src=r.result;};r.readAsDataURL(file);}
function readAsData(file,cb){const r=new FileReader();r.onload=()=>cb(r.result);r.readAsDataURL(file);}
$('fileAny').addEventListener('change',e=>{const f=e.target.files[0];if(!f)return;const isImg=(f.type||'').startsWith('image/');const cb=d=>{pendingAtts.push({name:f.name,data:d});addAttChip({name:f.name,data:d});};if(isImg)readAsImage(f,cb);else readAsData(f,cb);e.target.value='';});
function addPasted(f){if(!f)return;const isImg=(f.type||'').startsWith('image/');const cb=d=>{pendingAtts.push({name:f.name||'pasted',data:d});addAttChip({name:f.name||'pasted',data:d});};if(isImg)readAsImage(f,cb);else readAsData(f,cb);}
// 粘贴（图片/文件）
$('i').addEventListener('paste',e=>{const items=(e.clipboardData&&e.clipboardData.items)||[];const it=[].slice.call(items).find(x=>x.type&&x.type.startsWith('image/'));if(it){e.preventDefault();addPasted(it.getAsFile());}});
// 拖放（图片/文件）→ 加入附件
document.addEventListener('dragover',e=>e.preventDefault());
document.addEventListener('drop',e=>{e.preventDefault();const fs=(e.dataTransfer&&e.dataTransfer.files)||[];[].slice.call(fs).forEach(addPasted);});
const CMDS=[['/file','选择文件/图片，传给PC识别'],['/列目录','列出当前项目的目录结构'],['/找bug','分析这个项目，找出可能的 BUG 并给出修复'],['/写测试','为最近的代码改动补充单元测试'],['/总结','总结最近的 git 提交和改动要点'],['/重构','分析当前代码，给出重构建议'],['/解释','解释当前项目的作用和整体结构']];
function slashShow(q){
 const box=$('slash');
 const list=CMDS.filter(c=>!q||c[0].slice(1).toLowerCase().includes(q.toLowerCase())).slice(0,8);
 if(!list.length){box.classList.remove('open');return;}
 box.innerHTML=list.map(c=>'<div class="si" data-k="'+esc(c[0])+'" data-t="'+esc(c[1])+'"><span class="k">'+c[0]+'</span><span class="d">'+esc(c[1])+'</span></div>').join('');
 box.classList.add('open');
}
$('i').addEventListener('input',()=>{const v=$('i').value;if(v.startsWith('/'))slashShow(v.slice(1));else $('slash').classList.remove('open');});
$('slash').addEventListener('click',e=>{const si=e.target.closest('.si');if(!si)return;const k=si.dataset.k;
 if(k==='/file'){const i=$('i');i.value='';try{$('fileAny').click()}catch(_){}
 }else{const i=$('i');i.value=si.dataset.t;i.focus();}
 $('slash').classList.remove('open');});
$('i').addEventListener('keydown',e=>{if(e.key==='Escape')$('slash').classList.remove('open');});
$('f').onsubmit=ev=>{ev.preventDefault();
 const i=$('i');const t=i.value.trim();
 // /file 不当聊天消息发：激活上传（拉选择器，清空输入框）
 if(t.startsWith('/file')){ i.value='';cur=null;try{$('fileAny').click()}catch(_){}; $('slash').classList.remove('open'); return; }
 if(!t&&!pendingAtts.length)return;
 if(!sid)return;
 i.value='';cur=null;
 ws.send(JSON.stringify({type:'send',session:sid,text:t,atts:pendingAtts}));
 pendingAtts=[];try{$('pendingAtts').innerHTML=''}catch(_){};
};
connect();setInterval(loadState,8000);
