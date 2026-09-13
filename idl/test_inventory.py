import copy
import json
from pathlib import Path
import shutil
import tempfile
import unittest
import inventory

class InventoryTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup)
        self.root=Path(self.temp.name)
        repo=Path(__file__).resolve().parents[1]
        self.data=json.loads((repo/'idl/definitions.json').read_text())
        paths={'scripts/generate.targets'}
        for row in self.data['layers']:
            paths.update(row['api']+row['providers'])
            paths.update(r['source'] for r in row.get('native_rosters',[]))
            paths.update(d['path'] for d in row['definitions'])
        for rel in paths:
            dst=self.root/rel;dst.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(repo/rel,dst)

    def test_current_inventory_is_explicit(self):
        result=inventory.validate(self.root,self.data)
        self.assertEqual(result['layers'],13)
        self.assertEqual(result['descriptors_complete'],not result['pending'])
        if result['pending']:
            with self.assertRaisesRegex(ValueError,'obligations remain'):inventory.validate(self.root,self.data,True)

    def test_missing_descriptor_fails(self):
        (self.root/self.data['layers'][0]['definitions'][0]['path']).unlink()
        with self.assertRaisesRegex(ValueError,'missing descriptor|missing authoritative'):inventory.validate(self.root,self.data)

    def test_incorrect_ipc_flag_fails(self):
        p=self.root/'scripts/generate.targets';p.write_text(p.read_text().replace(' --no-ipc','').replace('\t--no-ipc',''))
        with self.assertRaisesRegex(ValueError,'incorrect IPC'):inventory.validate(self.root,self.data)

    def test_service_cannot_be_claimed_record_only(self):
        data=copy.deepcopy(self.data)
        entry=next(d for r in data['layers'] for d in r['definitions'] if d['mode']=='service')
        entry['mode']='records'
        with self.assertRaisesRegex(ValueError,'classification'):inventory.validate(self.root,data)

    def test_new_definition_cannot_be_omitted(self):
        p=self.root/'openabstractions-flat/abstraction-watch/unlisted.thrift';p.parent.mkdir(exist_ok=True,parents=True);p.write_text('struct New {}')
        with self.assertRaisesRegex(ValueError,'unclassified'):inventory.validate(self.root,self.data)

    def test_pending_interface_cannot_disappear(self):
        data=copy.deepcopy(self.data)
        row=next(r for r in data['layers'] if r['interface_generation']['status']=='pending')
        del row['interface_generation']
        with self.assertRaisesRegex(ValueError,'interface-generation'):inventory.validate(self.root,data,True)
        data=copy.deepcopy(self.data);data['layers'][0]['language_obligations']['go']={}
        with self.assertRaisesRegex(ValueError,'language obligations'):inventory.validate(self.root,data)

    def test_native_refusal_roster_drift_fails(self):
        row=next(r for r in self.data['layers'] if r.get('native_rosters'))
        path=self.root/row['native_rosters'][0]['source']
        path.write_text(path.read_text()+'\nconst CodeFuture = "future_refusal"\n')
        with self.assertRaisesRegex(ValueError,'native roster drift'):inventory.validate(self.root,self.data)

    def test_missing_layer_and_language_fail(self):
        data=copy.deepcopy(self.data);data['layers'].pop()
        with self.assertRaisesRegex(ValueError,'thirteen'):inventory.validate(self.root,data)
        data=copy.deepcopy(self.data);del data['layers'][0]['language_obligations']['cpp']
        with self.assertRaisesRegex(ValueError,'language obligations'):inventory.validate(self.root,data)

if __name__=='__main__':unittest.main()
