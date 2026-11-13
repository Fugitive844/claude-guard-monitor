'use strict';
const ipPrivacy=(()=>{
 const storageKey='claude-guard-show-addresses',originals=new WeakMap(),fields=new WeakMap();
 let visible=false,terminalRef=null;
 try{visible=localStorage.getItem(storageKey)==='true'}catch{}
 function mask(text,keepWidth=false){
  text=text.replace(/\[[0-9a-f:.%]+\](?::\d+)?|(?<![\w:])(?:[0-9a-f]{0,4}:){2,}[0-9a-f:.]*(?:%[\w.-]+)?/gi,value=>{
   const bracket=value.startsWith('['),address=(bracket?value.slice(1,value.indexOf(']')):value).split('%')[0];
   const normalized=address.replace(/(?:\d{1,3}\.){3}\d{1,3}$/, '0:0'),parts=normalized.split(':'),compressed=normalized.includes('::');
   if(!parts.every(p=>/^[0-9a-f]{0,4}$/i.test(p))||normalized.split('::').length>2||(!compressed&&parts.length!==8)||(compressed&&parts.filter(Boolean).length>=8))return value;
   if(keepWidth){
    const first=value.indexOf(':');
    return value.slice(0,first+1)+value.slice(first+1).replace(/[0-9a-z]/gi,'*');
   }
   const hidden=(address.split(':')[0]||'*')+':****';
   return bracket?'['+hidden+']'+(value.includes(']:')?':****':''):hidden;
  });
  return text.replace(/\b((?:\d{1,3}\.){3}\d{1,3})(:\d+)?\b/g,(value,address,port)=>{
   if(!address.split('.').every(n=>Number(n)<=255))return value;
   if(keepWidth){const first=value.indexOf('.');return value.slice(0,first+1)+value.slice(first+1).replace(/\d/g,'*')}
   return address.split('.')[0]+'.*.*.*'+(port?':****':'');
  });
 }
 function restoreOrMask(node,key,current,port=false){
  let entries=originals.get(node);if(!entries){entries=new Map();originals.set(node,entries)}
  const old=entries.get(key),raw=old&&current===old.shown?old.raw:current;
  const shown=visible?raw:port&&raw.trim()?'****':mask(raw);
  entries.set(key,{raw,shown});return shown;
 }
 function refresh(){
  observer.disconnect();
  document.documentElement.classList.toggle('hide-addresses',!visible);
  document.querySelectorAll('[data-ip-toggle]').forEach(el=>{el.checked=visible});
  const walker=document.createTreeWalker(document.body,NodeFilter.SHOW_TEXT);
  while(walker.nextNode()){
   const node=walker.currentNode,parent=node.parentElement;
   if(!parent||parent.closest('script,style,textarea,#sshTerminalScreen,[data-privacy-overlay]'))continue;
   const shown=restoreOrMask(node,'text',node.data,!!parent.closest('[data-ssh-port]'));
   if(node.data!==shown){if(parent.tagName==='OPTION'&&!parent.hasAttribute('value'))parent.value=node.data;node.data=shown}
  }
  document.querySelectorAll('[title],[placeholder],[aria-label]').forEach(el=>{
   if(el.closest('#sshTerminalScreen'))return;
   for(const attr of ['title','placeholder','aria-label'])if(el.hasAttribute(attr)){
    const value=el.getAttribute(attr),shown=restoreOrMask(el,attr,value);
    if(value!==shown)el.setAttribute(attr,shown);
   }
  });
  document.querySelectorAll('input:not([type=password]):not([type=hidden]),textarea').forEach(el=>{
   if(el.closest('#sshTerminalScreen'))return;
   const port=el.matches('#sshHostForm [name=port]'),hidden=port&&el.value?'****':mask(el.value);
   let state=fields.get(el);
   const conceal=!visible&&hidden!==el.value;
   if(!state&&!conceal)return;
   if(!state){
    const wrap=document.createElement('span');wrap.className='privacy-field';el.before(wrap);wrap.append(el);
    const cover=document.createElement('span');cover.dataset.privacyOverlay='';cover.className='privacy-field-cover';wrap.append(cover);
    state={wrap,cover,readOnly:el.readOnly};fields.set(el,state);
   }
   state.wrap.classList.toggle('concealed',conceal);state.cover.textContent=hidden;state.cover.hidden=!conceal;
   el.readOnly=conceal||state.readOnly;
  });
  observer.observe(document.body,{subtree:true,childList:true,characterData:true,attributes:true,attributeFilter:['title','placeholder','aria-label','open','value']});
 }
 const observer=new MutationObserver(refresh);
 function setVisible(value){visible=value;if(!visible)terminalRef?.clearSelection();try{localStorage.setItem(storageKey,String(value))}catch{}refresh();terminalRef?.refresh(0,terminalRef.rows-1)}
 function addToggle(parent){
  const label=document.createElement('label');label.className='privacy-toggle';
  const input=document.createElement('input');input.type='checkbox';input.dataset.ipToggle='';input.checked=visible;input.onchange=()=>setVisible(input.checked);
  label.append(input,document.createTextNode('显示 IP / SSH 端口'));
  if(parent.closest('dialog'))parent.insertBefore(label,parent.children[1]);else parent.append(label);
 }
 addToggle(document.querySelector('.header-right'));
 document.querySelectorAll('dialog > .panel-head,dialog > .ssh-editor-header').forEach(addToggle);
 const terminal=document.getElementById('sshTerminalScreen'),stage=document.createElement('div'),cover=document.createElement('div');
 stage.className='terminal-stage';terminal.before(stage);stage.append(terminal,cover);
 cover.className='terminal-privacy';cover.dataset.privacyOverlay='';cover.tabIndex=0;cover.title='点击输入命令；拖选文字后 Ctrl+C 复制；Ctrl+V 粘贴';
 const focusTerminal=()=>terminalRef?.textarea?.focus({preventScroll:true});
 let selecting=false;
 cover.addEventListener('pointerdown',()=>{selecting=true;window.getSelection()?.removeAllRanges()});
 cover.addEventListener('focus',()=>{if(!selecting)focusTerminal()});
 cover.addEventListener('pointerup',()=>{selecting=false;if(!window.getSelection()?.toString())focusTerminal()});
 cover.addEventListener('keydown',event=>{
  if(event.ctrlKey&&event.key.toLowerCase()==='c'&&window.getSelection()?.toString())return;
  focusTerminal();
  if(event.key.length>1||event.ctrlKey&&event.key.toLowerCase()!=='v'||event.altKey){
   event.preventDefault();terminalRef?.textarea?.dispatchEvent(new KeyboardEvent('keydown',{key:event.key,code:event.code,keyCode:event.keyCode,ctrlKey:event.ctrlKey,altKey:event.altKey,shiftKey:event.shiftKey,metaKey:event.metaKey,bubbles:true,cancelable:true}));
  }
 });
 cover.addEventListener('wheel',event=>{if(terminalRef){event.preventDefault();terminalRef.scrollLines(Math.sign(event.deltaY)*3)}},{passive:false});
 function renderTerminal(){
  if(visible||!terminalRef)return;
  // Mirror the bundled xterm DOM renderer so its colors, glyph widths and cursor stay identical.
  const rows=terminal.querySelector('.xterm-rows');if(!rows)return;
  const clone=rows.cloneNode(true),buffer=terminalRef.buffer.active;
  cover.className='terminal-privacy '+Array.from(terminalRef.element.classList).filter(name=>name.startsWith('xterm-dom-renderer-owner-')).join(' ');
  clone.removeAttribute('aria-hidden');
  let end=-1,raw='',masked='',offset=0;
  const nextRows=Array.from(clone.children);
  const rowText=nextRows.map(row=>row.textContent);
  const lineText=y=>rowText[y-buffer.viewportY]??buffer.getLine(y).translateToString(false);
  nextRows.forEach((row,index)=>{
   const y=buffer.viewportY+index;if(y>=buffer.length)return;
   if(y>end){
    let start=y;while(start>0&&buffer.getLine(start).isWrapped)start--;
    end=y;while(end+1<buffer.length&&buffer.getLine(end+1).isWrapped)end++;
    let text='';offset=0;
    for(let line=start;line<=end;line++){
     const value=lineText(line);
     if(line<y)offset+=value.length;
     text+=value;
    }
    raw=text;masked=mask(text,true);
   }
   // Mask rendered text to preserve invisible cells and the renderer's spacing.
   const localMasked=mask(rowText[index],true);
   const walker=document.createTreeWalker(row,NodeFilter.SHOW_TEXT);let column=offset;
   while(walker.nextNode()){
    const node=walker.currentNode,length=node.data.length;
    node.data=node.data.split('').map((char,i)=>raw[column+i]!==masked[column+i]?masked[column+i]:localMasked[column+i-offset]).join('');column+=length;
   }
   offset+=lineText(y).length;
  });
  const current=cover.firstElementChild;
  if(!current){cover.append(clone);return}
  current.className=clone.className;current.style.cssText=clone.style.cssText;
  // Keep unchanged rows mounted so focus/cursor updates do not clear a text selection.
  Array.from(clone.children).forEach((row,index)=>{
   const old=current.children[index];
   if(!old)current.append(row);else if(!old.isEqualNode(row))old.replaceWith(row);
  });
  while(current.children.length>nextRows.length)current.lastElementChild.remove();
 }
 document.addEventListener('input',event=>{if(!event.target.matches('[data-ip-toggle]'))refresh()});document.addEventListener('focusin',refresh);
 window.addEventListener('storage',event=>{if(event.key===storageKey){visible=event.newValue==='true';refresh();terminalRef?.refresh(0,terminalRef.rows-1)}});
 refresh();return {refresh,mask,renderTerminal,attachTerminal(term){terminalRef=term;if(!term)cover.textContent='';else term.refresh(0,term.rows-1)}};
})();
