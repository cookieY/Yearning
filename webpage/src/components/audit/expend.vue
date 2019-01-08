<template>
  <div>
    <Card>
      <p slot="title" style="height: 45px">
        <Icon type="android-send"></Icon>
        工单{{ this.$route.query.workid }}详细信息
        <br>
        <Button type="text" @click.native="$router.go(-1)">返回</Button>
      </p>
      <Table border :columns="tabcolumns" :data="TableDataNew" class="tabletop" style="background: #5cadff"
             size="large"></Table>
    </Card>
  </div>
</template>

<script>
  import axios from 'axios'

  export default {
    name: 'expend',
    data () {
      return {
        tabcolumns: [
          {
            title: '序号:',
            key: 'id',
            sortable: true
          },
          {
            title: '查询语句:',
            key: 'statements',
            sortable: true
          }
        ],
        TableDataNew: []
      }
    },
    mounted () {
      axios.post(`${this.$config.url}/query_worklf/`, {
        'workid': this.$route.query.workid,
        'user': this.$route.query.user
      })
        .then(res => {
          this.TableDataNew = res.data
        })
        .catch(error => {
          this.$config.err_notice(this, error)
        })
    }
  }
</script>

<style scoped>

</style>
