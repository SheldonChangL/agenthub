export namespace main {
	
	export class AnnounceStatus {
	    announceableAddresses: number;
	    // Go type: time
	    lastAttemptAt: any;
	    // Go type: time
	    lastAnnouncedAt: any;
	    lastError?: string;
	
	    static createFrom(source: any = {}) {
	        return new AnnounceStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.announceableAddresses = source["announceableAddresses"];
	        this.lastAttemptAt = this.convertValues(source["lastAttemptAt"], null);
	        this.lastAnnouncedAt = this.convertValues(source["lastAnnouncedAt"], null);
	        this.lastError = source["lastError"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Audience {
	    mode: string;
	    nodes?: string[];
	    exportCwd: boolean;
	    acceptMessages: boolean;
	    allowOutbound: boolean;
	    autoWake: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Audience(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mode = source["mode"];
	        this.nodes = source["nodes"];
	        this.exportCwd = source["exportCwd"];
	        this.acceptMessages = source["acceptMessages"];
	        this.allowOutbound = source["allowOutbound"];
	        this.autoWake = source["autoWake"];
	    }
	}
	export class Candidate {
	    nodeId: string;
	    address: string;
	    displayName?: string;
	    platform?: string;
	    fingerprint: string;
	    // Go type: time
	    firstSeen: any;
	    // Go type: time
	    lastSeen: any;
	    duplicate?: boolean;
	    contested?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Candidate(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.nodeId = source["nodeId"];
	        this.address = source["address"];
	        this.displayName = source["displayName"];
	        this.platform = source["platform"];
	        this.fingerprint = source["fingerprint"];
	        this.firstSeen = this.convertValues(source["firstSeen"], null);
	        this.lastSeen = this.convertValues(source["lastSeen"], null);
	        this.duplicate = source["duplicate"];
	        this.contested = source["contested"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ClearedInbox {
	    removed: number;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new ClearedInbox(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.removed = source["removed"];
	        this.error = source["error"];
	    }
	}
	export class InboxMessage {
	    id: string;
	    from: string;
	    body: string;
	    // Go type: time
	    createdAt: any;
	
	    static createFrom(source: any = {}) {
	        return new InboxMessage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.from = source["from"];
	        this.body = source["body"];
	        this.createdAt = this.convertValues(source["createdAt"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class InboxView {
	    sessionId: string;
	    messages: InboxMessage[];
	    held: number;
	    capacity: number;
	    full: boolean;
	    showing: number;
	    more: boolean;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new InboxView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sessionId = source["sessionId"];
	        this.messages = this.convertValues(source["messages"], InboxMessage);
	        this.held = source["held"];
	        this.capacity = source["capacity"];
	        this.full = source["full"];
	        this.showing = source["showing"];
	        this.more = source["more"];
	        this.error = source["error"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LocalAddress {
	    interface: string;
	    address: string;
	    subnet: string;
	    private: boolean;
	
	    static createFrom(source: any = {}) {
	        return new LocalAddress(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.interface = source["interface"];
	        this.address = source["address"];
	        this.subnet = source["subnet"];
	        this.private = source["private"];
	    }
	}
	export class NodeIdentity {
	    id: string;
	    displayName: string;
	    platform: string;
	    // Go type: time
	    createdAt: any;
	    publicKey?: string;
	    fingerprint?: string;
	
	    static createFrom(source: any = {}) {
	        return new NodeIdentity(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.displayName = source["displayName"];
	        this.platform = source["platform"];
	        this.createdAt = this.convertValues(source["createdAt"], null);
	        this.publicKey = source["publicKey"];
	        this.fingerprint = source["fingerprint"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Peer {
	    nodeId: string;
	    displayName: string;
	    online: boolean;
	    sequence?: number;
	    // Go type: time
	    receivedAt: any;
	    // Go type: time
	    expiresAt: any;
	    sessions: Session[];
	    sessionsWithheld?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Peer(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.nodeId = source["nodeId"];
	        this.displayName = source["displayName"];
	        this.online = source["online"];
	        this.sequence = source["sequence"];
	        this.receivedAt = this.convertValues(source["receivedAt"], null);
	        this.expiresAt = this.convertValues(source["expiresAt"], null);
	        this.sessions = this.convertValues(source["sessions"], Session);
	        this.sessionsWithheld = source["sessionsWithheld"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class TrustedNode {
	    nodeId: string;
	    displayName: string;
	    platform: string;
	    publicKey: string;
	    fingerprint: string;
	    // Go type: time
	    pairedAt: any;
	    // Go type: time
	    lastSeenAt: any;
	
	    static createFrom(source: any = {}) {
	        return new TrustedNode(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.nodeId = source["nodeId"];
	        this.displayName = source["displayName"];
	        this.platform = source["platform"];
	        this.publicKey = source["publicKey"];
	        this.fingerprint = source["fingerprint"];
	        this.pairedAt = this.convertValues(source["pairedAt"], null);
	        this.lastSeenAt = this.convertValues(source["lastSeenAt"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Session {
	    id: string;
	    provider: string;
	    providerSessionId: string;
	    management: string;
	    visibility: string;
	    audience: Audience;
	    status: string;
	    statusSource: string;
	    cwd?: string;
	    source?: string;
	    // Go type: time
	    lastSeenAt: any;
	    // Go type: time
	    updatedAt: any;
	
	    static createFrom(source: any = {}) {
	        return new Session(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.provider = source["provider"];
	        this.providerSessionId = source["providerSessionId"];
	        this.management = source["management"];
	        this.visibility = source["visibility"];
	        this.audience = this.convertValues(source["audience"], Audience);
	        this.status = source["status"];
	        this.statusSource = source["statusSource"];
	        this.cwd = source["cwd"];
	        this.source = source["source"];
	        this.lastSeenAt = this.convertValues(source["lastSeenAt"], null);
	        this.updatedAt = this.convertValues(source["updatedAt"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Overview {
	    node: NodeIdentity;
	    sessions: Session[];
	    nodes: TrustedNode[];
	    peers: Peer[];
	    presenceError?: string;
	    counts: Record<string, number>;
	    nodeCount: number;
	    nodeUrl: string;
	    reachable: boolean;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new Overview(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.node = this.convertValues(source["node"], NodeIdentity);
	        this.sessions = this.convertValues(source["sessions"], Session);
	        this.nodes = this.convertValues(source["nodes"], TrustedNode);
	        this.peers = this.convertValues(source["peers"], Peer);
	        this.presenceError = source["presenceError"];
	        this.counts = source["counts"];
	        this.nodeCount = source["nodeCount"];
	        this.nodeUrl = source["nodeUrl"];
	        this.reachable = source["reachable"];
	        this.error = source["error"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class PairingState {
	    open: boolean;
	    // Go type: time
	    openedAt: any;
	    // Go type: time
	    expiresAt: any;
	    remainingSeconds: number;
	    announcing: AnnounceStatus;
	    displayName: string;
	    nameIsChosen: boolean;
	
	    static createFrom(source: any = {}) {
	        return new PairingState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.open = source["open"];
	        this.openedAt = this.convertValues(source["openedAt"], null);
	        this.expiresAt = this.convertValues(source["expiresAt"], null);
	        this.remainingSeconds = source["remainingSeconds"];
	        this.announcing = this.convertValues(source["announcing"], AnnounceStatus);
	        this.displayName = source["displayName"];
	        this.nameIsChosen = source["nameIsChosen"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Pairing {
	    state: PairingState;
	    candidates: Candidate[];
	    full: boolean;
	    notice: string;
	    availability: string;
	    error?: string;
	    candidatesError?: string;
	
	    static createFrom(source: any = {}) {
	        return new Pairing(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.state = this.convertValues(source["state"], PairingState);
	        this.candidates = this.convertValues(source["candidates"], Candidate);
	        this.full = source["full"];
	        this.notice = source["notice"];
	        this.availability = source["availability"];
	        this.error = source["error"];
	        this.candidatesError = source["candidatesError"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	export class ServiceForm {
	    dbPath: string;
	    peerListen: string;
	    allowLan: boolean;
	    discover: boolean;
	    treatAsPrivate: string[];
	    autoWake: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ServiceForm(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.dbPath = source["dbPath"];
	        this.peerListen = source["peerListen"];
	        this.allowLan = source["allowLan"];
	        this.discover = source["discover"];
	        this.treatAsPrivate = source["treatAsPrivate"];
	        this.autoWake = source["autoWake"];
	    }
	}
	export class ServiceResult {
	    command: string;
	    output: string;
	
	    static createFrom(source: any = {}) {
	        return new ServiceResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.command = source["command"];
	        this.output = source["output"];
	    }
	}
	export class ServiceStatus {
	    tool: string;
	    toolError?: string;
	    supported: boolean;
	    installed: boolean;
	    running: boolean;
	    pid: number;
	    unitPath: string;
	    logHint: string;
	    nodeAnswering: boolean;
	    node: string;
	
	    static createFrom(source: any = {}) {
	        return new ServiceStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.tool = source["tool"];
	        this.toolError = source["toolError"];
	        this.supported = source["supported"];
	        this.installed = source["installed"];
	        this.running = source["running"];
	        this.pid = source["pid"];
	        this.unitPath = source["unitPath"];
	        this.logHint = source["logHint"];
	        this.nodeAnswering = source["nodeAnswering"];
	        this.node = source["node"];
	    }
	}
	
	
	export class VisibilityResult {
	    changed: number;
	    failed: number;
	    errors?: string[];
	
	    static createFrom(source: any = {}) {
	        return new VisibilityResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.changed = source["changed"];
	        this.failed = source["failed"];
	        this.errors = source["errors"];
	    }
	}

}

