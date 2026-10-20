package main

import (
	_ "embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static/panel-records.mjs
var panelRecordModule []byte

// These static codecs contain public definitions and no runtime observations.
func servePanelRecordCodecs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/record-codecs/")
	var body []byte
	if name == "panel-records.mjs" {
		body = panelRecordModule
	} else {
		if !strings.HasPrefix(name, "sdk/") || !strings.HasSuffix(name, ".mjs") {
			http.NotFound(w, r)
			return
		}
		name = strings.TrimPrefix(name, "sdk/")
		if !fs.ValidPath(name) {
			http.NotFound(w, r)
			return
		}
		var err error
		body, err = panelOwnedResources.ReadFile("static/native-sdk/" + name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

const panelRecordImports = `<script type="importmap">{"imports":{"@openabstractions/rights/internal":"/record-codecs/sdk/abstraction-rights/javascript/js/abstraction/rights/api/internal.mjs","@openabstractions/job-acceptance/internal":"/record-codecs/sdk/abstraction-job/javascript/js/abstraction/job/acceptance/internal.mjs"}}</script>`

const panelRecordScript = `
var oaRecordModule;
function oaRecords(){return oaRecordModule||(oaRecordModule=import('/record-codecs/panel-records.mjs'))}
async function oaPanelRequest(path,body){if(path.split('?')[0]==='/registry/selection'&&body&&body.candidates){const owner=await oaRecords();body={...body,candidates:await Promise.all(body.candidates.map(candidate=>owner.encode('inference','SelectionCandidateV2',candidate)))}}return body}
async function oaPanelResponse(path,value){
 const route=path.split('?')[0];
 if(route==='/card/table'){const owner=await oaRecords();value.resources=await Promise.all(value.resources.map(raw=>owner.decode('resource','ResourceState',raw)));for(const resource of value.resources)for(const holder of resource.holders)holder.title=(value.titles||{})[holder.program]||''}
 else if(route==='/card/storage/logs'){value=await(await oaRecords()).decode('logging','HistoryUsageResult',value)}
 else if(route==='/identity'&&value.runtime&&value.runtime.record){const owner=await oaRecords(),native=await owner.decode('facade','CallerObservation',value.runtime.record);value.runtime=Object.assign(native,{accountName:value.runtime.accountName,absent:value.runtime.absent,error:value.runtime.error})}
 else if(route==='/registry/selection'){const owner=await oaRecords();if(value.read)value.read=await owner.decode('inference','SelectionReadV2',value.read);if(value.change)value.change=await owner.decode('inference','SelectionChange',value.change)}
 if(route.startsWith('/setup/')&&value&&value.progress){const owner=await oaRecords();value.progress=Object.fromEntries(await Promise.all(Object.entries(value.progress).map(async([key,raw])=>[key,await owner.decode('job','WorkProgress',raw)])))}
 return value;
}
async function oaRecordDetails(value){return(await oaRecords()).details(value)}
`
