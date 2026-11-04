package.path='/usr/libexec/claude-guard/?.lua;'..package.path
require('guard').main(arg[1] or 'status')
