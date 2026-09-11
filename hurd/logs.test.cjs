const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const {readFileSync}=require('node:fs');
const source=readFileSync(__dirname+'/logs.js','utf8');
function fixture() {
  function element(){return {listeners:{},textContent:'',scrollTop:0,scrollHeight:100,checked:true,addEventListener(n,f){this.listeners[n]=f;}};}
  const elements={};
  for(const id of ['log-viewer','log-output','log-status','log-pause','log-refresh','log-follow'])elements[id]=element();
  elements['log-viewer'].dataset={instance:'camera one'};
  const document=Object.assign(element(),{hidden:false,getElementById(id){return elements[id];}});
  const requests=[];const timers=new Map();let next=0;
  vm.runInNewContext(source,{document,window:element(),URLSearchParams,AbortController,Date,
    setTimeout(f,ms){timers.set(++next,{f,ms});return next;},clearTimeout(id){timers.delete(id);},
    fetch(url,opts){return new Promise(resolve=>requests.push({url,opts,resolve}));}
  });
  return {elements,requests,timers,document,
    async reply(text='line',ok=true){requests.at(-1).resolve({ok,json:async()=>({text,updated:'2026-09-11T12:00:00Z',error:'journal failed'})});await new Promise(setImmediate);},
    click(id){elements[id].listeners.click();},
    poll(){const timer=[...timers.values()].find(t=>t.ms===2000);assert.ok(timer);timer.f();}
  };
}
test('fetches selected instance and displays log content as text',async()=>{
 const f=fixture();assert.equal(f.requests[0].url,'/setup/logs/tail?instance=camera+one');
 await f.reply('<script>literal log</script>');
 assert.equal(f.elements['log-output'].textContent,'<script>literal log</script>');
 assert.equal(f.elements['log-output'].scrollTop,100);
 f.elements['log-follow'].checked=false;f.elements['log-output'].scrollTop=12;
 f.poll();await f.reply('next');assert.equal(f.elements['log-output'].scrollTop,12);
});
test('pause cancels requests and rejects late responses; resume refreshes',async()=>{
 const f=fixture();f.click('log-pause');assert.equal(f.requests[0].opts.signal.aborted,true);
 await f.reply('late');assert.equal(f.elements['log-output'].textContent,'');
 assert.equal([...f.timers.values()].some(t=>t.ms===2000),false);
 f.click('log-pause');assert.equal(f.requests.length,2);await f.reply('resumed');
 assert.equal(f.elements['log-output'].textContent,'resumed');
});
test('hidden tabs stop polling; failed refreshes retain the previous snapshot',async()=>{
 const f=fixture();await f.reply('keep me');
 f.document.hidden=true;f.document.listeners.visibilitychange();
 assert.equal([...f.timers.values()].some(t=>t.ms===2000),false);
 f.document.hidden=false;f.document.listeners.visibilitychange();await f.reply('',false);
 assert.equal(f.elements['log-output'].textContent,'keep me');
 assert.match(f.elements['log-status'].textContent,/previous snapshot/);
});
test('changing the selector submits the bookmarkable filter in the current tab',()=>{
 let submitted=0;let changed;
 const filter={requestSubmit(){submitted++;}};
 const selector={addEventListener(name,handler){assert.equal(name,'change');changed=handler;}};
 vm.runInNewContext(source,{document:{getElementById(id){return {'log-filter':filter,'log-instance':selector}[id];}}});
 changed();assert.equal(submitted,1);
});
