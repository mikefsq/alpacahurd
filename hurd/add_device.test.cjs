const {test} = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const {readFileSync} = require('node:fs');
const source = readFileSync(__dirname + '/add_device.js', 'utf8');
function fixture() {
  const element = tag => ({tag, value:'', hidden:false, disabled:false, checked:false, dataset:{}, children:[], listeners:{},
    append(...children) {this.children.push(...children);}, replaceChildren(...children) {this.children=children;},
    addEventListener(event,fn) {this.listeners[event]=fn;}});
  const ids = ['device-form','driver','scan','save','error','status','details','results','name','fields','prototype','enable'];
  const elements = Object.fromEntries(ids.map(id => ['add-'+id,element(id)]));
  const pending = [], locations=[];
  vm.runInNewContext(source, {
    document:{getElementById:id=>elements[id], createElement:element, createTextNode:text=>({text})},
    window:{location:{assign:path=>locations.push(path)}},
    URLSearchParams, Option:function(text,value){return {text,value};},
    fetch(path,options){return new Promise(resolve=>pending.push({path,options,resolve}));}
  });
  const tick=()=>new Promise(resolve=>setImmediate(resolve));
  return {elements,pending,locations,tick, async load(data) {
    elements['add-driver'].value='test';elements['add-driver'].listeners.change();
    pending.at(-1).resolve({ok:true,json:async()=>data});await tick();
  }};
}
const discovered = () => ({token:'trusted-scan', prototype:'camera', values:{gain:23}, fields:[
  {name:'serial',label:'Serial',type:'text',default:''},
  {name:'index',label:'Index',type:'number',default:'0'},
  {name:'gain',label:'Gain',type:'number',default:'10'}
],discovery:{supported:true,identity:['serial','index'],devices:[
  {label:'Used',values:{serial:'A'},configured:'old-camera'},
  {label:'Available',values:{serial:'B',index:1}}
]}});
test('discovery offers configured devices as edit links and submits a selection without JSON',async()=>{
  const f=fixture();await f.load(discovered());
  const [used,available]=f.elements['add-results'].children;
  assert.equal(used.children.at(-1).tag,'a');
  assert.equal(used.children.at(-1).href,'/setup/edit?instance=old-camera');
  available.children.at(-1).listeners.click();
  assert.equal(f.elements['add-details'].hidden,false);
  assert.equal(f.elements['add-fields'].children.length,1); // identity comes from the scan
  assert.equal(f.elements['add-fields'].children[0].children[1].value,23);
  f.elements['add-device-form'].listeners.submit({preventDefault(){}});
  const req=f.pending.at(-1);
  assert.equal(req.path,'/setup/add');
  assert.equal(req.options.body.get('device'),'1');
  assert.equal(req.options.body.get('token'),'trusted-scan');
  assert.equal(req.options.body.has('field.gain'),false); // preserves existing setting
  assert.equal(req.options.body.has('text'),false);
  req.resolve({ok:true,json:async()=>({location:'/setup?saved=camera'})});await f.tick();
  assert.deepEqual(f.locations,['/setup?saved=camera']);
});
test('manual connection fields preserve the form when saving fails',async()=>{
  const f=fixture();await f.load({token:'manual',prototype:'mount',values:{},fields:[{name:'addr',label:'Address',type:'text',help:'host:port (3490, 3492)',default:''}],discovery:{supported:false,identity:['addr'],devices:[]}});
  const input=f.elements['add-fields'].children[0].children[1];
  input.value='mount.local:3492';input.listeners.input();
  f.elements['add-device-form'].listeners.submit({preventDefault(){}});
  const req=f.pending.at(-1);assert.equal(req.options.body.get('field.addr'),'mount.local:3492');
  req.resolve({ok:false,json:async()=>({error:'Already configured'})});await f.tick();
  assert.equal(input.value,'mount.local:3492');assert.equal(f.elements['add-error'].hidden,false);
  assert.equal(f.elements['add-error'].textContent,'Already configured');assert.equal(f.locations.length,0);
});
test('discovery failure permits retry and cannot submit a stale selection',async()=>{
  const f=fixture();await f.load(discovered());
  f.elements['add-results'].children[1].children.at(-1).listeners.click();
  f.elements['add-scan'].listeners.click();
  f.pending.at(-1).resolve({ok:false,json:async()=>({error:'Scan timed out'})});await f.tick();
  assert.equal(f.elements['add-details'].hidden,true);assert.match(f.elements['add-status'].textContent,/Unable to find/);assert.equal(f.elements['add-scan'].disabled,false);
  const count=f.pending.length;
  f.elements['add-device-form'].listeners.submit({preventDefault(){}});await f.tick();
  assert.equal(f.pending.length,count);
});
