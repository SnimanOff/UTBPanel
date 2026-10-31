import '@fontsource/ibm-plex-sans/400.css'
import '@fontsource/ibm-plex-sans/500.css'
import '@fontsource/jetbrains-mono/400.css'
import './styles/tokens.css'
import './styles/base.css'
import { mount } from 'svelte'
import App from './App.svelte'

mount(App, { target: document.getElementById('app')! })
