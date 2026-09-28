export async function api<T>(path: string, method='GET', body?: unknown): Promise<T> {
 const r = await fetch(`/v1/admin/${path}`,{method,credentials:'same-origin',headers:body===undefined?{}:{'Content-Type':'application/json'},body:body===undefined?undefined:JSON.stringify(body),signal:AbortSignal.timeout(15000)});
 const value=await r.json(); if (!r.ok) throw new Error(value.error || `HTTP ${r.status}`); return value as T;
}
export type Device={id:string;name:string;public_key:string;registered_at:number;disabled:boolean;owner?:string};
export type Peer={device:Device;online:boolean;last_seen:number};
export type Session={id:string;kind:string;from:string;to:string;paired:boolean;closed:boolean;expires_at:number};
export type Overview={devices:Peer[];online:number;sessions:Session[];version:string;backend:string;memory_bytes:number;goroutines:number;limitations:string[]};
export type User={id:string;name:string;role:string;disabled:boolean;created:number};
export type Node={id:string;name:string;address:string;region:string;enabled:boolean};
export type Release={manifest:{version:string;sequence:number;platform:string;sha256:string;expires_at:number;url:string};signature:string};
export type Audit={at:number;actor:string;action:string;object:string};
