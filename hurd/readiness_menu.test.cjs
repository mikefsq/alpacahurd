const {test} = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
function fixture() {
  const element = () => ({
    handlers: {}, style: {setProperty(){},removeProperty(){}},
    addEventListener(type,fn){ (this.handlers[type] ||= []).push(fn); },
    fire(type,event={}){ for(const fn of this.handlers[type]||[]) fn(event); },
    setAttribute(){},hasAttribute(){return false;},
    contains(node){ return node === this; },
    getBoundingClientRect(){ return {left:10,right:350,top:100,bottom:150,width:340}; },
    focus(){}, offsetWidth:340,offsetHeight:66
  });
  const menu=element(), panel=element(), trigger=element(), button=element();
  panel.contains = node => node === panel || node === button;
  panel.querySelector = () => button;
  menu.contains = node => node === menu || node === trigger;
  menu.querySelector = selector => selector === 'summary' ? trigger : panel;
  menu.closest = () => menu;
  const document=element();
  document.querySelectorAll = selector => selector === '.device-menu' ? [menu] : [];
  document.getElementById = () => null;
  document.documentElement=element();
  document.documentElement.clientHeight=700;
  document.body={appendChild(){}};
  const window=element();
  Object.assign(window,{innerHeight:700,innerWidth:390,matchMedia:()=>({matches:true})});
  const timers=[];
  vm.runInNewContext(fs.readFileSync(__dirname+'/readiness.js','utf8'),{window,document,setTimeout:fn=>timers.push(fn),clearTimeout(){}});
  menu.open=true;
  menu.fire('toggle');
  return {menu,panel,trigger,button,document,timers};
}
test('touch focus loss leaves menu available for native form submission',()=>{
  const {menu,panel,trigger,button,document}=fixture();
  trigger.fire('focusout',{relatedTarget:null});
  menu.fire('focusout',{relatedTarget:null});
  document.fire('pointerdown',{target:{closest:()=>panel}});
  panel.fire('focusout',{relatedTarget:null});
  assert.equal(menu.open,true);
  assert.equal(panel.hidden,false);
  assert.equal((button.handlers.click||[]).length,0); // native submit is not intercepted
});
test('outside tap and Escape still dismiss the menu',()=>{
  const {menu,panel,document}=fixture();
  document.fire('pointerdown',{target:{closest:()=>null}});
  assert.equal(menu.open,false);
  assert.equal(panel.hidden,true);
  menu.open=true; menu.fire('toggle');
  panel.fire('keydown',{key:'Escape',preventDefault(){}});
  assert.equal(menu.open,false);
});
