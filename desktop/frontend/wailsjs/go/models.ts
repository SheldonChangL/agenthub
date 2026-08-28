export namespace main {
	
	export class Audience {
	    mode: string;
	    nodes?: string[];
	    exportCwd: boolean;
	    acceptMessages: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Audience(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mode = source["mode"];
	        this.nodes = source["nodes"];
	        this.exportCwd = source["exportCwd"];
	        this.acceptMessages = source["acceptMessages"];
	    }
	}
	export class NodeIdentity {
	    id: string;
	    displayName: string;
	    platform: string;
	    // Go type: time
	    createdAt: any;
	
	    static createFrom(source: any = {}) {
	        return new NodeIdentity(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.displayName = source["displayName"];
	        this.platform = source["platform"];
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
	    counts: Record<string, number>;
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
	        this.counts = source["counts"];
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

