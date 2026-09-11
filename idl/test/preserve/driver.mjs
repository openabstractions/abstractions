import fs from 'node:fs';
import * as rec from './generated/js/rec.mjs';
try {
  const r=rec.decode(fs.readFileSync(process.argv[2])),scope=process.argv[4];
  if(scope==='mutate') {
    r.id='edited';if(r.child!==null)r.child.name='edited-child';for(const child of r.children)child.name='edited-child';
  } else if(scope!=='read') {
    const target=scope==='child'?r.child:scope==='repeated'?r.children[0]:r;
    target.extras[scope==='bad-key'?'\ud800':process.argv[5]]=fs.readFileSync(process.argv[6]);
  }
  const output=rec.encode(r);rec.decode(output);fs.writeFileSync(process.argv[3],output);process.stdout.write('ok');
} catch(e) {
  if(!(e instanceof rec.Refusal))throw e;
  process.stdout.write(e.word);
}
