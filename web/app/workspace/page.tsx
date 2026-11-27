"use client";
import {useCallback,useEffect,useState} from "react";
import {Download,FileText,FolderOpen,RefreshCw} from "lucide-react";
import WorkspaceShell from "../../components/workspace-shell";
import {api,type Task} from "../../lib/api";
type FileItem={intent_id:string;name:string;size:number;modified_at:string};
type Preview={intent_id:string;name:string;size:number;text_preview:string;preview_available:boolean};
export default function WorkspacePage(){
 const [tasks,setTasks]=useState<Task[]>([]),[taskID,setTaskID]=useState(""),[files,setFiles]=useState<FileItem[]>([]),[preview,setPreview]=useState<Preview|null>(null),[error,setError]=useState("");
 const load=useCallback(async()=>{try{setTasks(await api<Task[]>("/tasks?limit=100"));setError("");}catch(e){setError(e instanceof Error?e.message:"加载任务失败");}},[]);
 useEffect(()=>{void load();},[load]);
 useEffect(()=>{setTaskID(new URLSearchParams(window.location.search).get("task")||"");},[]);
 useEffect(()=>{setPreview(null);if(!taskID){setFiles([]);return;}void api<FileItem[]>(`/tasks/${taskID}/workspace`).then(setFiles).catch(e=>setError(e instanceof Error?e.message:"加载文件失败"));},[taskID]);
 async function open(file:FileItem){try{setPreview(await api<Preview>(`/tasks/${taskID}/workspace/${file.intent_id}/${encodeURIComponent(file.name)}`));setError("");}catch(e){setError(e instanceof Error?e.message:"预览失败");}}
 return <WorkspaceShell section="workspace"><header className="workspace-header"><div><span className="workspace-eyebrow">漏洞挖掘 / Workspace</span><h1>任务文件</h1><p>按挖掘任务和方向浏览 Worker 生成的文件与观察产物。</p></div><button className="workspace-secondary" type="button" onClick={()=>{void load();if(taskID)void api<FileItem[]>(`/tasks/${taskID}/workspace`).then(setFiles);}}><RefreshCw size={16}/>刷新</button></header>{error&&<div className="form-error" role="alert">{error}</div>}
 <div className="phase6-columns"><section className="workspace-card"><h2><FolderOpen size={18}/> 文件列表</h2><label>任务<select value={taskID} onChange={e=>setTaskID(e.target.value)}><option value="">选择任务</option>{tasks.map(t=><option key={t.id} value={t.id}>{t.title}</option>)}</select></label><div className="simple-list">{files.map(file=><button className="phase6-file" type="button" key={`${file.intent_id}/${file.name}`} onClick={()=>void open(file)}><FileText size={17}/><span><strong>{file.name}</strong><small>方向 {file.intent_id.slice(0,8)} · {file.size} 字节 · {new Date(file.modified_at).toLocaleString("zh-CN")}</small></span></button>)}{taskID&&!files.length&&<p className="empty-small">该任务尚无工作区文件</p>}</div></section><section className="workspace-card"><h2>文件预览</h2>{preview?<><p><strong>{preview.name}</strong> · {preview.size} 字节</p>{preview.preview_available?<pre className="phase6-preview">{preview.text_preview}</pre>:<p className="form-hint">二进制或空文件不提供文本预览。</p>}<a className="workspace-link" href={`/api/v1/tasks/${taskID}/workspace/${preview.intent_id}/${encodeURIComponent(preview.name)}?download=1`}><Download size={15}/>下载文件</a></>:<p className="empty-small">选择左侧文件查看内容。</p>}</section></div>
 </WorkspaceShell>;
}
