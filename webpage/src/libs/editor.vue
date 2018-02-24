<template>
  <div style="width: 100%;height: 200px"></div>
</template>

<script>
  require(['emmet/emmet'], function (data) {
    window.emmet = data.emmet;
  });
  const ace = require('brace');
  export default {
    name: 'editor',
    props: {
      value: {
        type: String,
        required: true
      }
    },
    data () {
      return {
        editor: null,
        contentBackup: ''
      }
    },
    watch: {
      value (val) {
        if (this.contentBackup !== val) {
          this.editor.setValue(val, 1);
        }
      },
      theme: function (newTheme) {
        this.editor.setTheme('ace/theme/' + newTheme);
      },
      lang: function (newLang) {
        this.editor.getSession().setMode('ace/mode/' + newLang);
      }
    },
    mounted () {
      let vm = this;
      require('brace/ext/emmet');
      require('brace/ext/language_tools');
      let editor = vm.editor = ace.edit(this.$el);
      this.$emit('init', editor);
      editor.setOptions({
        enableBasicAutocompletion: true,
        enableLiveAutocompletion: true})
      editor.$blockScrolling = Infinity;
      editor.setFontSize(14);
      editor.setOption('enableEmmet', true);
      editor.getSession().setMode('ace/mode/mysql');
      editor.setTheme('ace/theme/xcode');
      editor.setValue(this.value, 1);
      editor.on('change', function () {
        let content = editor.getValue();
        vm.$emit('input', content);
        vm.contentBackup = content;
      });
    }
  }
</script>

<style scoped>

</style>
