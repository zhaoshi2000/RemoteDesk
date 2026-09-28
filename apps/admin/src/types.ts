export interface Sample { at: string; cpu_percent: number|null; memory_total:number|null; memory_used:number|null; process_rss:number|null;disk_total:number|null;disk_used:number|null;network_rx:number|null;network_tx:number|null;rx_per_second:number|null;tx_per_second:number|null;heap_bytes:number;goroutines:number }
export interface DeviceMeta {alias:string;group:string;notes:string;disabled:boolean}
export interface Device {device:{id:string;name:string;public_key:string;registered_at:number};online:boolean;last_seen:number;candidates:{tcp:string[]|null;udp:string[]|null};meta:DeviceMeta}
export interface Session {id:string;kind:string;from:string;to:string;paired:boolean;closed:boolean;expires_at:number}
export interface Settings {node_name:string;notice:string;registration_open:boolean}
export interface Audit {at:string;actor:string;action:string;target:string;result:string}
export interface Release {version:string;url:string;sha256:string;notes:string;published_at?:string}
export interface Component {name:string;state:string;listen:string;detail:string}
export interface Overview {server_time:string;version:string;server:{name:string;hostname:string;status:string;heartbeat_at:string;started_at:string;uptime_seconds:number;os:string;arch:string;go_version:string;cpu_cores:number;components:Component[];sample:Sample;total_requests:number;store:string;external_reachability:string};managed_devices:Device[];online:number;disabled:number;sessions:Session[];active_ssh_relay:number;active_direct_p2p:null;notice:string;media_relay:null|{status:string;listen:string;advertise:string;valid_grants:number;both_sides_seen:number;received_payload_bytes:number;forwarded_payload_bytes:number;forwarded_packets:number};limitations:string[]}
