import "./style.css";
import backdrop from "./assets/images/hacker-bg.jpg";
import * as bindings from "../wailsjs/go/main/App";
import { configure, boot } from "./app.js";

configure(bindings);
boot({ backdropUrl: backdrop });
