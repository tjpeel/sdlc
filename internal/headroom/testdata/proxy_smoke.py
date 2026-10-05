import asyncio,json,os,subprocess,threading,time,urllib.request
from fastapi import FastAPI, Request, WebSocket
from fastapi.responses import JSONResponse,StreamingResponse
import uvicorn,httpx
from websockets.sync.client import connect
from headroom.transforms.lossless_compaction import expand_runs
app=FastAPI(); captured=[]
fake_bearer=' '.join(('Bearer', 'fake-disposable-fixture'))
usage={'input_tokens':42,'output_tokens':7,'input_tokens_details':{'cached_tokens':11},'output_tokens_details':{'reasoning_tokens':2}}
response={'id':'resp_fixture','object':'response','model':'gpt-4o','status':'completed','output':[{'type':'message','id':'msg_fixture','role':'assistant','status':'completed','content':[{'type':'output_text','text':'fixture','annotations':[]}]}],'usage':usage}
resp_events=[{'type':'response.created','response':dict(response,status='in_progress',output=[],usage=None)}, {'type':'response.output_item.done','output_index':0,'item':response['output'][0]}, {'type':'response.completed','response':response}]
anthropic_usage={'input_tokens':42,'output_tokens':7,'cache_read_input_tokens':11,'cache_creation_input_tokens':3}
anthropic={'id':'msg_fixture','type':'message','role':'assistant','model':'claude-sonnet-4','content':[{'type':'text','text':'fixture'}],'stop_reason':'end_turn','stop_sequence':None,'usage':anthropic_usage}
@ app.post('/v1/responses')
async def responses(r:Request):
 b=await r.json();captured.append(('responses',b,dict(r.headers)))
 if b.get('model')=='fixture-error':return JSONResponse({'error':{'type':'rate_limit_error','message':'fixture quota'}},status_code=429,headers={'retry-after':'1'})
 if b.get('stream'):
  return StreamingResponse(iter('event: '+e['type']+'\ndata: '+json.dumps(e)+'\n\n' for e in resp_events),media_type='text/event-stream')
 return JSONResponse(response)
@app.post('/v1/messages')
async def messages(r:Request):
 b=await r.json();captured.append(('messages',b,dict(r.headers)))
 if b.get('stream'):
  events=[{'type':'message_start','message':dict(anthropic,content=[],usage={'input_tokens':42,'output_tokens':0,'cache_read_input_tokens':11,'cache_creation_input_tokens':3})},{'type':'content_block_start','index':0,'content_block':{'type':'text','text':''}},{'type':'content_block_delta','index':0,'delta':{'type':'text_delta','text':'fixture'}},{'type':'content_block_stop','index':0},{'type':'message_delta','delta':{'stop_reason':'end_turn','stop_sequence':None},'usage':{'output_tokens':7}},{'type':'message_stop'}]
  return StreamingResponse(iter('event: '+e['type']+'\ndata: '+json.dumps(e)+'\n\n' for e in events),media_type='text/event-stream')
 return JSONResponse(anthropic)
@app.websocket('/v1/responses')
async def ws(socket:WebSocket):
 await socket.accept();b=await socket.receive_json();captured.append(('ws',b,dict(socket.headers)))
 for event in resp_events:await socket.send_text(json.dumps(event))
 await socket.close()
server=uvicorn.Server(uvicorn.Config(app,host='127.0.0.1',port=8900,log_level='critical'))
threading.Thread(target=server.run,daemon=True).start()
flags=json.loads(os.environ['SMOKE_PROXY_FLAGS'])+['--openai-api-url','http://127.0.0.1:8900','--anthropic-api-url','http://127.0.0.1:8900']
with open('/tmp/proxy.log','w') as log:
 p=subprocess.Popen(['headroom','proxy']+flags,stdout=log,stderr=log)
 try:
  for _ in range(180):
   if p.poll() is not None:raise RuntimeError('proxy exited '+str(p.returncode))
   try:
    if httpx.get('http://127.0.0.1:8787/readyz',timeout=1).status_code==200:break
   except Exception:pass
   time.sleep(.25)
  else:raise RuntimeError('readiness timeout')
  client=httpx.Client(base_url='http://127.0.0.1:8787',timeout=30)
  body={'model':'gpt-4o','input':[{'role':'user','content':'fixture request'}],'text':{'format':{'type':'json_schema','name':'fixture','strict':True,'schema':{'type':'object','properties':{'ok':{'type':'boolean'}},'required':['ok'],'additionalProperties':False}}},'tools':[{'type':'function','name':'fixture_tool','parameters':{'type':'object','properties':{'path':{'type':'string'}},'required':['path']}}]}
  r=client.post('/v1/responses',json=body,headers={'authorization':fake_bearer});assert r.status_code==200,(r.status_code,r.text);assert r.json()['usage']==usage
  assert captured[-1][1]['text']==body['text'];assert captured[-1][2]['authorization']==fake_bearer;assert not any(t.get('name')=='headroom_retrieve' for t in captured[-1][1]['tools'])
  r=client.post('/v1/responses',json=dict(body,stream=True));assert r.status_code==200 and 'response.completed' in r.text;assert json.loads([l[6:] for l in r.text.splitlines() if l.startswith('data: ')][-1])['response']['usage']==usage
  abody={'model':'claude-sonnet-4','max_tokens':32,'messages':[{'role':'user','content':'fixture request'}]}
  r=client.post('/v1/messages',json=abody,headers={'anthropic-beta':'oauth-2025-04-20','authorization':fake_bearer});assert r.status_code==200,(r.status_code,r.text);assert r.json()['usage']==anthropic_usage;assert 'oauth-2025-04-20' in captured[-1][2]['anthropic-beta']
  r=client.post('/v1/messages',json=dict(abody,stream=True));assert r.status_code==200 and 'message_stop' in r.text and '"output_tokens": 7' in r.text
  r=client.post('/v1/responses',json=dict(body,model='fixture-error'));assert r.status_code==429,(r.status_code,r.text);assert r.json()['error']['type']=='rate_limit_error'
  with connect('ws://127.0.0.1:8787/v1/responses',open_timeout=5) as socket:
   socket.send(json.dumps(dict(body,type='response.create')))
   events=[]
   for _ in range(3):events.append(json.loads(socket.recv(timeout=10)))
   assert events[-1]['type']=='response.completed' and events[-1]['response']['usage']==usage,events
  logs="2026-01-01T00:00:00Z INFO fixture: test passed\n"*1000
  logbody=dict(abody,messages=[{'role':'user','content':'run tests'},{'role':'assistant','content':[{'type':'tool_use','id':'tool_fixture','name':'Bash','input':{'command':'test'}}]},{'role':'user','content':[{'type':'tool_result','tool_use_id':'tool_fixture','content':logs}]}],tools=[{'name':'Bash','description':'run fixture command','input_schema':{'type':'object','properties':{'command':{'type':'string'}}}}])
  r=client.post('/v1/messages',json=logbody);assert r.status_code==200,(r.status_code,r.text)
  sent=captured[-1][1]['messages'][-1]['content'][0];assert sent['tool_use_id']=='tool_fixture';assert '<<ccr:' not in sent['content']
  if os.environ.get('SMOKE_MODE')=='passthrough':assert sent['content']==logs
  else:
   assert len(sent['content'])<len(logs),(len(sent['content']),len(logs))
   assert expand_runs(sent['content'])==logs, 'compressed tool logs did not round-trip'
  stats=client.get('/stats').json();print(json.dumps({'mode':os.environ.get('SMOKE_MODE','optimize'),'probes':['readiness','responses-json-schema','responses-sse-usage','messages-oauth-header','messages-sse-usage','upstream-429','responses-websocket-usage','lossless-tool-log-roundtrip'],'requests':stats.get('requests'),'tokens':stats.get('tokens')}))
 finally:
  p.terminate()
  try:p.wait(timeout=10)
  except subprocess.TimeoutExpired:p.kill();p.wait()
