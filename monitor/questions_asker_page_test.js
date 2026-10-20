// The Questions page names an application question's asker by the display
// name derived from its bound program, with the program in the title and
// Technical details (asks ASK-A2, ASK-O11). This runs the page's own
// questionSentence from rights_page.go.
const fs=require('fs'),vm=require('vm'),assert=require('assert');
const source=fs.readFileSync(__dirname+'/rights_page.go','utf8');
const start=source.indexOf('function questionSentence(');
assert(start>=0,'questionSentence is in the page script');
const end=source.indexOf('\n}',start)+2;
const context={};vm.createContext(context);vm.runInContext(source.slice(start,end),context);
const q={Text:'opencode.exe wants to fetch from example.com',About:'example.com'};
const first=context.questionSentence(q,undefined,undefined,{name:'opencode.exe',program:'C:\\one\\opencode.exe'});
const second=context.questionSentence(q,undefined,undefined,{name:'opencode.exe',program:'C:\\two\\opencode.exe'});
assert.equal(first.line,q.Text+'\nRequested by opencode.exe');
assert.equal(first.title,'C:\\one\\opencode.exe');
assert.equal(first.raw,'Program: C:\\one\\opencode.exe');
assert.notEqual(first.raw,second.raw,'two programs with one file name read apart');
const older=context.questionSentence(q,undefined,undefined,undefined);
assert.deepEqual({...older},{line:q.Text,title:'',raw:null},'a question without a recorded program reads unchanged');
const firstUse=context.questionSentence(q,undefined,'App wants to look up models.',{name:'serve.exe',program:'C:\\oa\\serve.exe'});
assert.equal(firstUse.line,'App wants to look up models.','a first-use sentence keeps its own wording');
