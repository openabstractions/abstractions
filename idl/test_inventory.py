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
        paths={'scripts/generate.targets',inventory.CONTRACT_RULES_RECORDED}
        for row in self.data['layers']:
            paths.update(row['api']+row['providers'])
            paths.update(r['source'] for r in row.get('native_rosters',[]))
            paths.update(d['path'] for d in row['definitions'])
        paths.update(p for p in inventory.metadata_paths(repo) if (repo/p).is_file())
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

    def test_missing_provider_source_fails(self):
        row=next(r for r in self.data['layers'] if r['layer']=='facade')
        self.assertIn('openabstractions-flat/abstraction-facade/go-core/resolution/host.go',row['providers'])
        (self.root/row['providers'][0]).unlink()
        with self.assertRaisesRegex(ValueError,'facade: missing authoritative source'):inventory.validate(self.root,self.data)

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
        # Every current interface is described; removing any declaration must still refuse.
        row=next(r for r in data['layers'] if 'interface_generation' in r)
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

    def test_generated_package_requires_metadata(self):
        (self.root/'openabstractions-flat/abstraction-router/javascript/package.json').unlink()
        with self.assertRaisesRegex(ValueError,'lack metadata.*abstraction-router/javascript/package.json'):inventory.validate(self.root,self.data)
        p=self.root/'scripts/generate.targets';p.write_text(p.read_text()+'openabstractions-flat/abstraction-watch/watch.thrift openabstractions-flat/abstraction-watch/fresh rust\n')
        with self.assertRaisesRegex(ValueError,'abstraction-watch/fresh/rust/Cargo.toml'):inventory.validate(self.root,self.data)

    def test_metadata_exemption_must_stay_current(self):
        data=copy.deepcopy(self.data);data['unpackaged_outputs']['openabstractions-flat/abstraction-router/py/pyproject.toml']='claimed'
        with self.assertRaisesRegex(ValueError,'exempted package metadata exists'):inventory.validate(self.root,data)
        data=copy.deepcopy(self.data);data['unpackaged_outputs']['openabstractions-flat/abstraction-none/py/pyproject.toml']='claimed'
        with self.assertRaisesRegex(ValueError,'stale package metadata exemption'):inventory.validate(self.root,data)
        data=copy.deepcopy(self.data);key=next(iter(data['unpackaged_outputs']));data['unpackaged_outputs'][key]=''
        with self.assertRaisesRegex(ValueError,'needs a reason'):inventory.validate(self.root,data)

RULE_FIXTURE='''
struct Doc { 1: required string text } (doc="enum Hidden { 1: x } inside a string is prose")
// enum Commented { 1: y }
enum GoodOutcome { 1: done 2: forbidden 3: unavailable 4: invalid 5: unknown 6: conflict } (unknown = "refuse", reader = "act")
enum ThinOutcome { 1: done 2: forbidden } (unknown="refuse",reader="act")
struct GoodResult {
  1: required GoodOutcome outcome
} (unknown_fields = "refuse")
struct ThinResult {
  1: required ThinOutcome outcome
}
struct Bare { 1: required string value }
struct RequestIdentity { 1: required string key }
struct Ask {
  1: required string asker
  2: required string question (catalogue = "closed", closed_by = "ASK-K1")
  3: required string topic
} (unknown_fields = "refuse")
struct Keys { 1: required string store } (catalogue = "open")
enum TransferCause { 1: other 2: timeout } (unknown = "grant", reader = "display")
enum StrictCause { 1: timeout } (unknown = "refuse", reader = "act")
enum Colour { 1: red }
const list<string> widget_actions = ["a", "b"]
const list<string> sealed_keys = ["k"] (catalogue = "closed", closed_by = "CFG-R2")
service Fixture {
  GoodResult Replace(1: RequestIdentity identity, 2: string expected_revision) (doc="gated (conditional) write")
  ThinResult Thin(1: string cursor) (doc="lacks refusals")
  Bare Plain() (doc="no outcome")
  oneway void Fire(1: Bare value)
}
'''

class ContractRuleTests(unittest.TestCase):
    def violations(self,text=RULE_FIXTURE,service=True,pending=()):
        return {(r,s):d for r,_,s,d in inventory.definition_violations('x.thrift',inventory.contract_model(text),service,pending_catalogues=pending)}

    def test_model_ignores_strings_and_comments(self):
        model=inventory.contract_model(RULE_FIXTURE)
        self.assertNotIn('Hidden',model['enums']);self.assertNotIn('Commented',model['enums'])
        self.assertEqual(set(model['services']['Fixture']),{'Replace','Thin','Plain','Fire'})
        self.assertEqual(model['consts']['sealed_keys'],{'catalogue':'closed','closed_by':'CFG-R2'})
        self.assertEqual(model['structs']['Ask']['fields'][1],('string','question',{'catalogue':'closed','closed_by':'ASK-K1'}))
        self.assertEqual(model['structs']['Keys']['annotations'],{'catalogue':'open'})
        self.assertEqual(model['enums']['TransferCause']['reader'],'display')

    def test_r1_outcome_enum_and_reserved_refusals(self):
        found=self.violations()
        self.assertNotIn(('R1','Fixture.Replace'),found)
        self.assertEqual(found[('R1','Fixture.Thin')],'ThinOutcome lacks unavailable, invalid')
        self.assertIn('without an outcome enum',found[('R1','Fixture.Plain')])
        self.assertIn('without an outcome enum',found[('R1','Fixture.Fire')])
        # A record-addressing call reserves unknown; a conditional write reserves conflict.
        lean=RULE_FIXTURE.replace('5: unknown 6: conflict','')
        self.assertEqual(self.violations(lean)[('R1','Fixture.Replace')],'GoodOutcome lacks unknown, conflict')
        # Direct interfaces have no transport call to judge.
        self.assertFalse([k for k in self.violations(service=False) if k[0]=='R1'])

    def test_r3_cause_enums_grant_with_other(self):
        found=self.violations()
        self.assertNotIn(('R3','TransferCause'),found)
        self.assertIn(('R3','StrictCause'),found)
        self.assertIn(('R3','NoOtherCause'),self.violations(RULE_FIXTURE+'enum NoOtherCause { 1: late } (unknown="grant")\n'))

    def test_r4_catalogue_is_declared_open_or_closed(self):
        found=self.violations()
        # A catalogue-named const must declare itself; a closed one names its rule.
        self.assertIn('undeclared',found[('R4','widget_actions')])
        self.assertNotIn(('R4','sealed_keys'),found)
        self.assertNotIn(('R4','Ask.question'),found)
        # An open catalogue needs a Register* method in the same definition.
        self.assertIn('no Register*',found[('R4','Keys')])
        opened=RULE_FIXTURE.replace('service Fixture {','service Fixture {\n  GoodResult RegisterStore(1: string name)')
        self.assertNotIn(('R4','Keys'),self.violations(opened))
        # A closed catalogue without its rule tag, and a value the rule does not define.
        untagged=RULE_FIXTURE.replace(', closed_by = "ASK-K1"','')
        self.assertIn('no closed_by',self.violations(untagged)[('R4','Ask.question')])
        self.assertIn('defines open and closed',self.violations(RULE_FIXTURE.replace('catalogue = "open"','catalogue = "ajar"'))[('R4','Keys')])
        # Struct and field catalogues are named by the ratchet, never by their names.
        self.assertNotIn(('R4','Ask.topic'),found)
        pending=self.violations(pending={'Ask.topic','Bare','Ask.question'})
        self.assertIn('undeclared',pending[('R4','Ask.topic')])
        self.assertIn('undeclared',pending[('R4','Bare')])
        self.assertNotIn(('R4','Ask.question'),pending)

    def test_r7_reader_marker_decides_grant_or_refuse(self):
        found=self.violations()
        self.assertIn('no reader',found[('R7','Colour')])
        self.assertFalse([k for k in found if k[0]=='R7' and k[1]!='Colour'])
        acted=self.violations(RULE_FIXTURE+'enum Verdict { 1: a } (unknown="grant", reader="act")\n')
        self.assertIn('requires unknown = "refuse"',acted[('R7','Verdict')])
        shown=self.violations(RULE_FIXTURE+'enum Label { 1: a } (unknown="refuse", reader="display")\n')
        self.assertIn('requires unknown = "grant"',shown[('R7','Label')])
        self.assertIn('no reader',self.violations(RULE_FIXTURE+'enum Maybe { 1: a } (unknown="refuse", reader="glance")\n')[('R7','Maybe')])


class ContractRuleRatchetTests(unittest.TestCase):
    setUp=InventoryTests.setUp

    def test_new_violation_in_a_real_definition_fails(self):
        p=self.root/'openabstractions-flat/abstraction-config/config.thrift'
        p.write_text(p.read_text(encoding='utf-8')+'\nenum EditCause { 1: disk } (unknown = "refuse", reader = "act")\n',encoding='utf-8')
        with self.assertRaisesRegex(ValueError,'new contract rule violations: R3 .*config.thrift EditCause'):inventory.validate(self.root,self.data)

    def test_recorded_violation_that_no_longer_occurs_fails(self):
        p=self.root/inventory.CONTRACT_RULES_RECORDED
        p.write_text(p.read_text(encoding='utf-8')+'R7\topenabstractions-flat/abstraction-config/config.thrift\tGone\tfixed\n',encoding='utf-8')
        with self.assertRaisesRegex(ValueError,'no longer occur.*R7 openabstractions-flat/abstraction-config/config.thrift Gone'):inventory.validate(self.root,self.data)

    def test_annotating_a_recorded_catalogue_shrinks_the_backlog(self):
        p=self.root/'openabstractions-flat/abstraction-asks/asks.thrift'
        text=p.read_text(encoding='utf-8')
        self.assertIn(' 2: required string key\n',text)
        p.write_text(text.replace(' 2: required string key\n',' 2: required string key(catalogue="closed",closed_by="ASK-K1")\n',1),encoding='utf-8')
        with self.assertRaisesRegex(ValueError,'no longer occur.*asks.thrift Question.key'):inventory.validate(self.root,self.data)

    def test_marked_outcome_that_changes_policy_fails(self):
        p=self.root/'openabstractions-flat/abstraction-rights/rights.thrift'
        text=p.read_text(encoding='utf-8')
        self.assertIn('(unknown="refuse",reader="act")',text)
        p.write_text(text.replace('(unknown="refuse",reader="act")','(unknown="grant",reader="act")',1),encoding='utf-8')
        with self.assertRaisesRegex(ValueError,'new contract rule violations: R7 .*rights.thrift DecisionOutcome'):inventory.validate(self.root,self.data)

    def test_recorded_backlog_matches_current_definitions(self):
        recorded=inventory.read_recorded(self.root/inventory.CONTRACT_RULES_RECORDED)
        found=inventory.contract_violations(self.root,self.data)
        self.assertEqual(recorded,set(found))
        self.assertEqual(inventory.validate(self.root,self.data)['contract_rule_backlog'],len(found))
        # The definitions outside the 0.1.7 closure carry every marker already.
        outside=('abstraction-rights/','abstraction-identity/','abstraction-watch/','abstraction-cas/')
        self.assertFalse([v for v in found if v[0]=='R7' and any(o in v[1] for o in outside)])

if __name__=='__main__':unittest.main()
