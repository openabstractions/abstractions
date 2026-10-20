"""Source-owned native core/Swift byte projection; no UI or runtime contact."""
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
FIXTURE = r'''
func require(_ value: @autoclosure () -> Bool, _ message: String) { if !value() { fatalError(message) } }
extension NativeAppModel {
    func fixtureClosed() -> Bool { model == nil && session == nil }
}
let manifest = try Data(contentsOf: URL(fileURLWithPath: CommandLine.arguments[1]))
var replies: [Data] = [], activations: [(UInt64, UInt64, Data)] = []
let owner = NativeAppModel(instance: "source-owned-instance", manifest: manifest,
    output: { replies.append($0); return true }, activation: { activations.append(($0,$1,$2));return true })!
let request = #"{"jsonrpc":"2.0","id":17,"method":"tools/call","params":{"name":"open","arguments":{ "page" : "credentials" }}}"#
require(owner.receive(Data(request.utf8)), "native input")
require(activations.count == 1 && activations[0].1 == 1 && replies.isEmpty, "native callback precedes reply")
require(String(data:activations[0].2,encoding:.utf8) == #"{ "page" : "credentials" }"#, "original payload span")
var accepted = true
owner.complete(String(activations[0].0,radix:16), result: Data(#"{"outcome":"applied","reason":"forged","extra":true}"#.utf8)) { accepted=$0 }
require(accepted && replies.count == 1, "malformed native callback retires through common fault")
owner.complete(String(activations[0].0,radix:16), result: Data(#"{"outcome":"applied","reason":""}"#.utf8)) { accepted=$0 }
require(!accepted && replies.count == 1, "terminal callback cannot be replaced")
require(owner.receive(Data(request.replacingOccurrences(of:"17",with:"171").utf8)), "next independent callback")
owner.complete(String(activations.last!.0,radix:16), result: Data(#"{"outcome":"applied","reason":""}"#.utf8)) { accepted=$0 }
require(accepted && replies.count == 2, "native canonical terminal reply")
let rpc = try JSONSerialization.jsonObject(with:replies[1]) as! [String:Any]
let result = rpc["result"] as! [String:Any], content = result["content"] as! [[String:Any]]
let canonical = try JSONSerialization.jsonObject(with:Data((content[0]["text"] as! String).utf8)) as! [String:Any]
require(canonical["outcome"] as? String == "applied" && canonical["reason"] as? String == "", "canonical generated host result")
let before = activations.count
require(owner.receive(Data(#"{"jsonrpc":"2.0","id":18,"method":"tools/call","params":{"name":"open","arguments":{"page":"work"}}}"#.utf8)), "invalid input gets native response")
require(activations.count == before && replies.count == 3, "schema refusal before UI")
require(owner.receive(Data(request.replacingOccurrences(of:"17",with:"19").utf8)), "pending original callback")
let pending = activations.last!.0
owner.retire()
require(!owner.receive(Data(request.utf8)), "EOF cannot start replacement mediation")
let count = replies.count
owner.complete(String(pending,radix:16),result:Data(#"{"outcome":"unavailable","reason":"fixture page closed"}"#.utf8)) { accepted=$0 }
require(accepted && replies.count == count, "issued callback completes without retired wire output")
owner.close();require(owner.fixtureClosed(), "native session/model released after completion")
owner.close();require(owner.fixtureClosed(), "idempotent native close")
extension NativeOwnedChannel {
    func fixtureModelClosed() -> Bool {
        lock.lock(); let current=model as? NativeAppModel; lock.unlock()
        return current?.fixtureClosed() == true
    }
}
let incoming=Pipe(), outgoing=Pipe(), announced=DispatchSemaphore(value:0), withdrawn=DispatchSemaphore(value:0)
let recordLock=NSLock()
var records:[[String:Any]]=[]
let channel=NativeOwnedChannel(input:incoming.fileHandleForReading,output:outgoing.fileHandleForWriting,
    send:{ record in recordLock.lock();records.append(record);recordLock.unlock();if record["kind"] as? String == "owned-bootstrap"{announced.signal()};return true },
    createPresence:{OpaquePointer(bitPattern:1)},announcePresence:{_,destination,capacity in
        let value=Array("original-announced-instance".utf8CString);require(value.count<=capacity,"instance buffer")
        for (index,byte) in value.enumerated(){destination[index]=byte};return 0
    },closePresence:{_ in withdrawn.signal()})
channel.start();require(announced.wait(timeout: .now() + 2) == .success,"original Announce")
require(!channel.configureModel(instance:"foreign-instance",manifest:manifest),"foreign bootstrap cannot select native instance")
require(channel.configureModel(instance:"original-announced-instance",manifest:manifest),"signed host configures actual common core")
require(!channel.configureModel(instance:"original-announced-instance",manifest:manifest),"no replacement native core")
require(channel.receiveModel(Data(request.utf8)),"actual signed-host projection receives input")
recordLock.lock();let first=records.last(where:{$0["kind"] as? String == "owned-core-activation"})!;recordLock.unlock()
let firstID=first["id"] as! String
channel.completeModel(firstID,body:Data(#"{"outcome":"applied","reason":""}"#.utf8))
recordLock.lock()
let acknowledgement=records.firstIndex(where:{$0["kind"] as? String == "owned-core-completed" && $0["id"] as? String == firstID})!
let emitted=records.firstIndex(where:{$0["kind"] as? String == "owned-core-output"})!
require(acknowledgement<emitted,"actual acknowledgement precedes new emissions")
require(records[acknowledgement]["error"]==nil,"actual core accepted callback")
recordLock.unlock()
require(channel.receiveModel(Data(request.replacingOccurrences(of:"17",with:"20").utf8)),"second original callback")
recordLock.lock();let late=records.last(where:{$0["kind"] as? String == "owned-core-activation"})!;let beforeOutputs=records.filter{$0["kind"] as? String == "owned-core-output"}.count;recordLock.unlock()
channel.retire();require(!channel.live(),"original EOF retires mediation")
channel.completeModel(late["id"] as! String,body:Data(#"{"outcome":"unavailable","reason":"fixture page ended"}"#.utf8))
recordLock.lock();require(records.filter{$0["kind"] as? String == "owned-core-output"}.count==beforeOutputs,"late original callback creates no retired wire output");recordLock.unlock()
channel.close();require(channel.fixtureModelClosed(),"signed host releases original native storage")
require(withdrawn.wait(timeout: .now() + 2) == .success,"original presence closes")
try incoming.fileHandleForWriting.close();try outgoing.fileHandleForReading.close()
print("PASS native AppModel -> Swift activation/result, raw span, schema, EOF and owned close controls")
'''

FAULT_FIXTURE = r'''
func require(_ value: @autoclosure () -> Bool, _ message: String) { if !value() { fatalError(message) } }
extension NativeAppModel {
    func fixtureClosed() -> Bool { model == nil && session == nil }
    func fixtureOtherCarrierTools() throws -> Data {
        guard let model else { fatalError("original model required") }
        var other: OpaquePointer?
        let made=Self.span(Data("other-carrier".utf8)){name in Self.span(Data("0.8.0".utf8)){version in oa_appmodel_session_create(model,name,version,&other)}}
        require(made==OA_APPMODEL_OK,"second carrier")
        defer { require(oa_appmodel_session_destroy(other)==OA_APPMODEL_OK,"second carrier closed") }
        var responding:Int32=0
        let request=Data(#"{"jsonrpc":"2.0","id":90,"method":"tools/list"}"#.utf8)
        let received=Self.span(request){oa_appmodel_session_receive(other,oa_appmodel_request_id(model),$0,&responding)}
        require(received==OA_APPMODEL_OK,"second carrier lists native tools")
        // tools/list uses app_describe, so receive its reply through the actual
        // host event pump with this fixture carrier, then restore the original.
        let original=session;session=other
        defer { session=original }
        let emissions=try pump()
        require(emissions.output.count==1,"second carrier reply")
        return emissions.output[0]
    }
}
let manifest=try Data(contentsOf:URL(fileURLWithPath:CommandLine.arguments[1]))
let request=Data(#"{"jsonrpc":"2.0","id":51,"method":"tools/call","params":{"name":"open","arguments":{"page":"credentials"}}}"#.utf8)
for bad in [Data(),Data("{".utf8),Data(#"{"outcome":"applied","reason":"forged"}"#.utf8)] {
    var replies:[Data]=[],calls:[UInt64]=[]
    let owner=NativeAppModel(instance:"fault-owner",manifest:manifest,output:{replies.append($0);return true},activation:{call,_,_ in calls.append(call);return true})!
    require(owner.receive(request) && calls.count==1,"issued original callback")
    var accepted=false
    owner.complete(String(calls[0],radix:16),result:bad){accepted=$0}
    require(accepted && replies.count==1,"malformed callback consumed as terminal fault")
    let rpc=try JSONSerialization.jsonObject(with:replies[0]) as! [String:Any]
    require((rpc["error"] as? [String:Any])?["code"] as? Int == -32603 && rpc["result"]==nil,"canonical native internal fault")
    owner.fault(String(calls[0],radix:16)){accepted=$0}
    require(!accepted && replies.count==1,"duplicate fault refused")
    owner.close();require(owner.fixtureClosed(),"fault releases original storage")
    owner.fault(String(calls[0],radix:16)){accepted=$0};require(!accepted,"closed owner refuses stale completion")
}
var replies:[Data]=[],calls:[UInt64]=[]
let owner=NativeAppModel(instance:"late-fault-owner",manifest:manifest,output:{replies.append($0);return true},activation:{call,_,_ in calls.append(call);return true})!
require(owner.receive(Data(#"{"jsonrpc":"2.0","id":89,"method":"tools/list"}"#.utf8)),"configured carrier tools")
let own=(try JSONSerialization.jsonObject(with:replies.removeLast()) as! [String:Any])["result"] as! [String:Any]
require((own["tools"] as! [[String:Any]]).contains{$0["name"] as? String == "open"},"configured carrier keeps selected UI activation")
let other=(try JSONSerialization.jsonObject(with:owner.fixtureOtherCarrierTools()) as! [String:Any])["result"] as! [String:Any]
require(!(other["tools"] as! [[String:Any]]).contains{$0["name"] as? String == "open"},"UI catalogue cannot leak between carriers")
require(owner.receive(request) && calls.count==1,"late issued callback")
owner.retire();var accepted=false
owner.fault(String(calls[0],radix:16)){accepted=$0}
require(accepted && replies.isEmpty,"late fault completes without retired wire")
owner.close();require(owner.fixtureClosed(),"late fault storage released")
extension NativeOwnedChannel {
    func fixtureModelClosed() -> Bool { lock.lock(); let value=model as? NativeAppModel;lock.unlock();return value?.fixtureClosed()==true }
}
let incoming=Pipe(),outgoing=Pipe(),announced=DispatchSemaphore(value:0),withdrawn=DispatchSemaphore(value:0)
let recordLock=NSLock();var records:[[String:Any]]=[]
let channel=NativeOwnedChannel(input:incoming.fileHandleForReading,output:outgoing.fileHandleForWriting,
    send:{record in recordLock.lock();records.append(record);recordLock.unlock();if record["kind"] as? String == "owned-bootstrap"{announced.signal()};return true},
    createPresence:{OpaquePointer(bitPattern:1)},announcePresence:{_,destination,capacity in
        let bytes=Array("original-instance".utf8CString);require(bytes.count<=capacity,"instance buffer")
        for (index,byte) in bytes.enumerated(){destination[index]=byte};return 0
    },closePresence:{_ in withdrawn.signal()})
channel.start();require(announced.wait(timeout: .now() + 2) == .success,"original native instance")
require(channel.configureModel(instance:"original-instance",manifest:manifest),"actual owned channel config")
require(channel.receiveModel(request),"actual owned channel receives callback")
recordLock.lock();let issued=records.last(where:{$0["kind"] as? String == "owned-core-activation"})!;recordLock.unlock()
channel.faultModel(issued["id"] as! String)
recordLock.lock()
let ack=records.firstIndex(where:{$0["kind"] as? String == "owned-core-completed"})!
let output=records.firstIndex(where:{$0["kind"] as? String == "owned-core-output"})!
require(ack<output && records[ack]["error"]==nil,"fault acknowledges actual owner before output")
let wire=Data(base64Encoded:records[output]["body"] as! String)!
recordLock.unlock()
let failure=(try JSONSerialization.jsonObject(with:wire) as! [String:Any])["error"] as! [String:Any]
require(failure["code"] as? Int == -32603,"owned channel preserves native internal fault")
channel.close();require(channel.fixtureModelClosed(),"owned channel releases faulted storage")
require(withdrawn.wait(timeout: .now() + 2) == .success,"owned presence closes")
try incoming.fileHandleForWriting.close();try outgoing.fileHandleForReading.close()
print("PASS native AppModel -> Swift fault, duplicate, late and session catalogue controls")
'''


@unittest.skipUnless(sys.platform == "darwin", "native Swift projection requires macOS")
class NativeAppModelProjection(unittest.TestCase):
    def test_native_callback_result_and_retirement(self):
        self.receive_fixture(FIXTURE)

    def test_native_fault_and_session_catalogue(self):
        self.receive_fixture(FAULT_FIXTURE)

    def receive_fixture(self, fixture):
        selected = os.environ.get("OA_PANEL_APPMODEL_SOURCE_ROOT", "")
        if not selected:
            self.skipTest("explicit source-owned native roots required")
        sources = Path(selected)
        self.assertTrue(sources.is_absolute())
        facade = sources / "abstraction-facade/cpp"
        with tempfile.TemporaryDirectory(prefix="oa-panel-appmodel-") as directory:
            root = Path(directory)
            source = root / "main.swift"
            source.write_text((HERE / "PanelLauncher.swift").read_text(encoding="utf-8") + "\n" + fixture,encoding="utf-8")
            includes = ["-I", str(facade / "include"), "-I", str(facade),
                        "-I", str(sources / "abstraction-download/cpp"),
                        "-I", str(sources / "abstraction-router/cpp"),
                        "-I", str(sources / "abstraction-rights/cpp")]
            objects = []
            for name, path in [("core",facade / "src/appmodel.cpp"),("mcp",facade / "src/appmodel_mcp.cpp"),("session",facade / "src/appmodel_session.cpp"),("host",HERE / "NativeHost.cpp")]:
                output = root / (name + ".o")
                subprocess.run(["/usr/bin/clang++","-std=c++17","-O1",*includes,"-c",str(path),"-o",str(output)],check=True,timeout=120)
                objects.append(str(output))
            stubs = root / "presence.c"
            stubs.write_text('#include "'+str(HERE / 'NativePresence.h')+'"\n'
                             'oa_panel_presence *oa_panel_presence_create(void){return 0;}\n'
                             'int oa_panel_presence_announce(oa_panel_presence*p,char*s,size_t n){return 1;}\n'
                             'void oa_panel_presence_close(oa_panel_presence*p){}\n',encoding='utf-8')
            stub_object=root/'presence.o'
            subprocess.run(['/usr/bin/clang','-c',str(stubs),'-o',str(stub_object)],check=True,timeout=30)
            program=root/'fixture'
            subprocess.run(['/usr/bin/swiftc','-D','PANEL_CORE_FIXTURE','-module-cache-path',str(root/'modules'),'-Xcc','-I'+str(facade/'include'),'-import-objc-header',str(HERE/'NativeHost.h'),str(source),*objects,str(stub_object),'-lc++','-o',str(program)],check=True,timeout=180)
            result=subprocess.run([str(program),str(HERE.parent/'panel-application.json')],capture_output=True,text=True,timeout=15)
            self.assertEqual(result.returncode,0,result.stdout+result.stderr)
            self.assertIn('PASS native AppModel -> Swift',result.stdout)
            print(result.stdout.strip())


if __name__ == "__main__": unittest.main()
