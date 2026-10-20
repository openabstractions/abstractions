// Execute the actual Questions renderer with an inert DOM. No browser, native
// dialog, runtime or model is started.
const fs=require('fs'),vm=require('vm'),assert=require('assert');
const source=fs.readFileSync(__dirname+'/rights_page.go','utf8');
const start=source.indexOf('async function questionsPass(){'),end=source.indexOf("\nel('questions-next').onclick",start);
assert(start>=0&&end>start);
function element(){return {children:[],textContent:'',append(...items){this.children.push(...items)},replaceChildren(){this.children=[]}}}
async function render({provider=true,state='not_written',option='allow'}={}){
 const fields=new Map(),actions=[];const el=id=>{if(!fields.has(id))fields.set(id,element());return fields.get(id)};
 const id='native-question',record={ID:id,Option:option,Options:['allow','refuse']};
 const page={Outcome:'page',Records:[record],Complete:true,Next:'',ruleStates:{[id]:state},nativePrompts:provider?{[id]:true}:{}};
 const context={encodeURIComponent,document:{createElement:element},rightsListReady:Promise.resolve(),questionCursor:'',questionsShown:'',questionsFailed:false,questionItems:new Map(),ruleStateText:{},el,call:async()=>page,waitingPages:async()=>page,renderWaitingMore(){},show(){},rightsKeep:(map,key,make)=>make(),rightsKept(){},questionSentence:()=>({line:'App provider request',title:'',raw:null}),questionChoiceList:()=>[],questionAct:action=>actions.push(JSON.parse(JSON.stringify(action))),rightsItem:value=>value,emptyLine(){},renderQuestionsEmpty(){},invalidateQuestions(){},fail:(id,error)=>{throw error}};
 vm.createContext(context);vm.runInContext(source.slice(start,end),context);await context.questionsPass();
 return {waiting:el('questions-list').children,history:el('questions-history-list').children,actions};
}
(async()=>{
 const provider=await render();assert.equal(provider.waiting.length,1);assert.equal(provider.history.length,0);
 assert.deepEqual(Array.from(provider.waiting[0].controls,b=>b.textContent),['Finish setup']);
 provider.waiting[0].controls[0].onclick();assert.deepEqual(provider.actions,[{action:'finish_provider',id:'native-question'}]);
 const written=await render({state:'written'});assert.equal(written.waiting.length,0);assert.deepEqual(Array.from(written.history[0].controls,b=>b.textContent),['Check setup']);written.history[0].controls[0].onclick();assert.deepEqual(written.actions,[{action:'finish_provider',id:'native-question'}]);
 const refused=await render({option:'refuse',state:'invalid'});assert.equal(refused.waiting.length,0);assert(!refused.history[0].controls.some(b=>b.textContent==='Finish setup'));
 const ordinary=await render({provider:false});
 const retry=ordinary.history[0].controls.find(b=>b.textContent==='Retry saving permission');
 assert(retry);
 retry.onclick();
 assert.deepEqual(ordinary.actions,[{action:'rule',id:'native-question'}]);
 console.log('provider_setup_page_test: ok');
})().catch(error=>{console.error(error);process.exit(1)});
