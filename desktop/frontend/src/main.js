import "./style.css";
import * as bindings from "../wailsjs/go/main/App";
import { configure, boot } from "./app.js";

configure(bindings);
boot();
