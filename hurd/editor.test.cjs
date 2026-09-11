const {test} = require('node:test');
const assert = require('node:assert/strict');
const {readFileSync} = require('node:fs');
const vm = require('node:vm');
const source = readFileSync(__dirname + '/editor.js', 'utf8');

function fixture(draft = '{"driver":"sim-camera"}') {
  function element(value) {
    return {value, disabled:false, listeners:{}, addEventListener(type, handler) {this.listeners[type]=handler;}};
  }
  const text = element(draft);
  const instance = element('cam');
  const form = element();
  form.elements = {namedItem(name) {return {text,instance}[name];}};
  const elements = {'config-editor':form,'check-config':element(),'save-config':element(),'syntax-status':element(),'config-status':element()};
  const pending = [];
  const context = {
    document:{getElementById(id) {return elements[id];}},
    window:element(), AbortController, URLSearchParams, setTimeout, clearTimeout,
    fetch(url,options) {return new Promise(resolve => pending.push({url,options,resolve}));}
  };
  vm.runInNewContext(source,context);
  return {
    text, form, pending, check:elements['check-config'],save:elements['save-config'],
    syntax:elements['syntax-status'],status:elements['config-status'],
    edit(value) {text.value=value;text.listeners.input();},
    reply(valid = true, ready = true) {pending.at(-1).resolve({ok:valid,json:async()=>({valid,ready,message:valid?'Configuration valid':'Bad configuration'})});},
    submit() {let prevented=false;form.listeners.submit({preventDefault(){prevented=true;}});return prevented;}
  };
}

test('JSONC syntax preserves comment-like strings and rejects malformed input', () => {
  const f=fixture();
  for(const draft of [
    '{/* comment */"driver":"sim-camera"}',
    '{// comment\n"name":"http://host/*literal*/", "escaped":"a\\\"b"}',
    '{"driver":"sim-camera"} // end',
  ]) {
    f.edit(draft);assert.equal(f.check.disabled,false,draft);assert.equal(f.syntax.className,'banner ok');
  }
  for(const draft of ['null','[]','{} /*\n','{"a":1,}','{"a":"unfinished}', '/* only comment */']) {
    f.edit(draft);assert.equal(f.check.disabled,true,draft);assert.equal(f.save.disabled,true);
  }
});

test('only a successful check of the current buffer enables saving', async () => {
  const f=fixture();assert.equal(f.save.disabled,true);assert.equal(f.submit(),true);
  const checking=f.check.listeners.click();
  assert.equal(f.pending[0].url,'/setup/edit/check');
  assert.equal(f.pending[0].options.body.get('text'),f.text.value);
  f.reply();await checking;
  assert.equal(f.save.disabled,false);
  f.edit('{"driver":"sim-camera","name":"changed"}');
  assert.equal(f.save.disabled,true);assert.equal(f.submit(),true);
  const rechecking=f.check.listeners.click();f.reply();await rechecking;
  assert.equal(f.submit(),false);assert.equal(f.save.disabled,true);
});

test('a stale successful response cannot enable Save after editing', async () => {
  const f=fixture();const checking=f.check.listeners.click();
  f.edit('{"driver":"sim-camera","name":"new"}');
  assert.equal(f.pending[0].options.signal.aborted,true);
  f.reply();await checking;assert.equal(f.save.disabled,true);
  assert.equal(f.status.textContent,'Configuration has not been checked.');
});

test('failed checks keep the draft and allow another check', async () => {
  const f=fixture();const draft=f.text.value;
  const checking=f.check.listeners.click();f.reply(false);await checking;
  assert.equal(f.text.value,draft);assert.equal(f.save.disabled,true);
  assert.equal(f.check.disabled,false);assert.match(f.status.textContent,/Bad configuration/);
});

test('a saveable disabled draft is shown as a warning, not ready', async () => {
  const f=fixture('{"driver":"sim-camera","enable":false}');
  const checking=f.check.listeners.click();f.reply(true,false);await checking;
  assert.equal(f.save.disabled,false);
  assert.equal(f.status.className,'banner warn');
  assert.equal(f.submit(),false);
});
